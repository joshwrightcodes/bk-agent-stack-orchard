// Package telemetry starts the OpenTelemetry providers of the controller, and it holds the
// instruments and the trace identity that the rest of the controller uses.
//
// All telemetry of this controller is OpenTelemetry, and it leaves through OTLP to a collector.
// There is no Prometheus endpoint, and there is no second mechanism for a metric, because a
// backend behind the collector can give any of them (PR-025).
//
// The standard OTEL_ environment variables decide the export. OTEL_EXPORTER_OTLP_ENDPOINT names
// the collector, OTEL_EXPORTER_OTLP_PROTOCOL chooses gRPC or HTTP, and OTEL_TRACES_EXPORTER with
// the value "none" stops one signal. An operator therefore changes the export with the variables
// of the specification, and this package holds no settings of its own for it.
//
// Three rules of the target design, section 10, live in the types of this package:
//
//   - The trace identifier of a job is the 128 bits of its Buildkite UUID, so every span of a
//     job is in one trace, across ticks and across a restart (see WithJob).
//   - No metric attribute carries a job identifier or the name of a virtual machine, which the
//     signatures of Metrics make true (invariant I-7).
//   - A log record goes to OTLP and to stdout as JSON, and it carries the trace when a span is
//     active, so an operator with a terminal and an operator with a backend read the same thing.
//     Both destinations are exporters of one logger provider, so the two carry the same record
//     in the same shape, which is the log data model of OpenTelemetry and not the shape of the
//     handler of the standard library.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/processors/minsev"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// The keys of the log fields that the target design, section 10.3, names. A constant keeps one
// spelling, because a search for a job in a backend needs one.
//
// Three of them are the CI/CD semantic conventions, because Buildkite has the shape that those
// conventions describe: a pipeline holds runs, and a run holds task runs, which is what
// Buildkite calls a job. A person who holds a backend with several CI systems can then ask one
// question of all of them.
//
// The rest carry the prefix of this project. The Mac host is one of them: the worker of the
// conventions is the thing that does the work of a job, which here is the virtual machine, so
// the name of the host needs a key that cannot be read as that worker.
const (
	LogJob      = string(semconv.CICDPipelineTaskRunIDKey)
	LogPipeline = string(semconv.CICDPipelineNameKey)
	LogBuild    = string(semconv.CICDPipelineRunIDKey)
	LogVM       = prefix + "vm.name"
	LogProfile  = attributeProfile
	LogWorker   = prefix + "worker.name"
)

// DefaultServiceName names this controller when nothing else does.
const DefaultServiceName = "bk-agent-stack-orchard"

// Options configures the providers.
type Options struct {
	// ServiceName and Version describe this process to a backend. OTEL_SERVICE_NAME and
	// OTEL_RESOURCE_ATTRIBUTES can replace them, because the resource reads the environment
	// after these values.
	ServiceName string
	Version     string

	// ControllerID separates two controllers that serve two queues in one backend. It is the
	// controller identifier of the configuration, and it becomes service.instance.id.
	ControllerID string

	// Output takes the JSON log records. A nil writer gives os.Stdout.
	Output io.Writer

	// Level is the lowest level that the logger writes. The zero value is slog.LevelInfo.
	Level slog.Leveler
}

// Telemetry holds what the controller needs to record its work, and the way to stop it.
type Telemetry struct {
	// Tracer starts the spans of the target design, section 10.2.
	Tracer trace.Tracer

	// Metrics holds the instruments of the target design, section 10.1.
	Metrics *Metrics

	// Logger writes to OTLP and to stdout.
	Logger *slog.Logger

	shutdown []func(context.Context) error
}

// Setup builds the providers and makes them global, so that a library with its own
// instrumentation, such as otelhttp, reports to the same collector.
//
// The caller MUST call Shutdown, because an exporter holds records in a queue, and the records
// of the last seconds of the process are the interesting ones.
func Setup(ctx context.Context, options Options) (*Telemetry, error) {
	if options.ServiceName == "" {
		options.ServiceName = DefaultServiceName
	}

	described, err := describe(ctx, options)
	if err != nil {
		return nil, err
	}

	telemetry := &Telemetry{}

	tracer, err := telemetry.traces(ctx, described)
	if err != nil {
		return nil, telemetry.abandon(ctx, err)
	}

	meter, err := telemetry.metrics(ctx, described, options.ServiceName)
	if err != nil {
		return nil, telemetry.abandon(ctx, err)
	}

	logger, err := telemetry.logs(ctx, described, options)
	if err != nil {
		return nil, telemetry.abandon(ctx, err)
	}

	instruments, err := NewMetrics(meter)
	if err != nil {
		return nil, telemetry.abandon(ctx, err)
	}

	// A context carries the trace between this process, the collector, and the agent inside a
	// virtual machine. W3C trace context is what the agent reads.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	telemetry.Tracer = tracer
	telemetry.Metrics = instruments
	telemetry.Logger = logger

	return telemetry, nil
}

// describe builds the resource that every signal carries.
func describe(ctx context.Context, options Options) (*resource.Resource, error) {
	attributes := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName(options.ServiceName),
			semconv.ServiceVersion(options.Version),
		),
	}

	if options.ControllerID != "" {
		attributes = append(attributes,
			resource.WithAttributes(semconv.ServiceInstanceID(options.ControllerID)))
	}

	// The environment comes last, so that OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES win
	// over the values of this process. An operator can then name a deployment without a change
	// of the configuration file.
	attributes = append(attributes,
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)

	described, err := resource.New(ctx, attributes...)
	if err != nil {
		// A resource that partly failed is better than no telemetry, and the SDK gives one with
		// the attributes that it could read.
		if described == nil || !errors.Is(err, resource.ErrPartialResource) {
			return nil, fmt.Errorf("describing this process to the collector: %w", err)
		}
	}

	return described, nil
}

func (t *Telemetry) traces(ctx context.Context, described *resource.Resource) (trace.Tracer, error) {
	exporter, err := spanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("building the span exporter: %w", err)
	}

	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(described)}

	// A signal that stays here still gets a provider, so that the spans exist and the log
	// records of this process carry their trace.
	if exporter != nil {
		options = append(options, sdktrace.WithBatcher(exporter))
	}

	provider := sdktrace.NewTracerProvider(options...)

	t.shutdown = append(t.shutdown, provider.Shutdown)
	otel.SetTracerProvider(provider)

	return provider.Tracer(DefaultServiceName), nil
}

func (t *Telemetry) metrics(ctx context.Context, described *resource.Resource,
	service string,
) (metric.Meter, error) {
	reader, err := metricReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("building the metric reader: %w", err)
	}

	options := []sdkmetric.Option{sdkmetric.WithResource(described)}
	if reader != nil {
		options = append(options, sdkmetric.WithReader(reader))
	}

	provider := sdkmetric.NewMeterProvider(options...)

	t.shutdown = append(t.shutdown, provider.Shutdown)
	otel.SetMeterProvider(provider)

	return provider.Meter(service), nil
}

func (t *Telemetry) logs(ctx context.Context, described *resource.Resource,
	options Options,
) (*slog.Logger, error) {
	exporter, err := logExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("building the log exporter: %w", err)
	}

	output := options.Output
	if output == nil {
		output = os.Stdout
	}

	// Stdout is the second destination of every record, and it is what an operator with a
	// terminal reads when the collector is the thing that failed. It writes as each record
	// arrives, because a record that waits in a batch is of no use to a person who watches.
	console, err := stdoutlog.New(stdoutlog.WithWriter(output))
	if err != nil {
		return nil, fmt.Errorf("building the stdout log exporter: %w", err)
	}

	level := options.Level
	if level == nil {
		level = slog.LevelInfo
	}

	// The severity of the processor, and not a handler, decides what a record costs: a record
	// below the level stops before it reaches either destination.
	least := minsev.Severity(level.Level())

	settings := []sdklog.LoggerProviderOption{
		sdklog.WithResource(described),
		sdklog.WithProcessor(minsev.NewLogProcessor(sdklog.NewSimpleProcessor(console), least)),
	}

	if exporter != nil {
		settings = append(settings, sdklog.WithProcessor(
			minsev.NewLogProcessor(sdklog.NewBatchProcessor(exporter), least)))
	}

	provider := sdklog.NewLoggerProvider(settings...)

	t.shutdown = append(t.shutdown, provider.Shutdown)
	global.SetLoggerProvider(provider)

	logger := slog.New(otelslog.NewHandler(options.ServiceName,
		otelslog.WithLoggerProvider(provider)))
	slog.SetDefault(logger)

	return logger, nil
}

// Shutdown stops the providers and sends what they hold. It gives the errors of all of them, so
// that one exporter that fails does not hide another.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var problems []error

	// Reverse order: the providers stop in the order that is opposite to their start.
	for i := len(t.shutdown) - 1; i >= 0; i-- {
		if err := t.shutdown[i](ctx); err != nil {
			problems = append(problems, err)
		}
	}

	t.shutdown = nil

	return errors.Join(problems...)
}

// abandon stops what already started, and gives the failure that stopped the rest.
func (t *Telemetry) abandon(ctx context.Context, cause error) error {
	if err := t.Shutdown(ctx); err != nil {
		return errors.Join(cause, err)
	}

	return cause
}
