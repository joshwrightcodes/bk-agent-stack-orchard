package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// This file builds the exporters, and it builds only OTLP ones.
//
// The OpenTelemetry contrib repository has a package that reads the exporter variables and
// builds whatever they name. This controller does not use it, because it also carries a
// Prometheus exporter, and the target design, section 10, states that the controller MUST NOT
// have a Prometheus endpoint. With that package, one environment variable would open one, and a
// rule that an environment variable can break is not a rule. The cost is the small amount of
// code below, which reads the two variables that decide the export.
//
// Everything else about the export stays with the SDK: OTEL_EXPORTER_OTLP_ENDPOINT, the headers,
// the compression, the timeout, and the certificates are what the exporters read themselves.

// The values that this controller accepts for a signal.
const (
	exporterOTLP = "otlp"
	exporterNone = "none"
)

// The OTLP transports of the specification.
const (
	protocolGRPC = "grpc"
	protocolHTTP = "http/protobuf"
)

// signal is one kind of telemetry, whose variables carry its name.
type signal string

const (
	traces  signal = "TRACES"
	metrics signal = "METRICS"
	logs    signal = "LOGS"
)

// wanted reports whether a signal leaves this process.
//
// The value "none" is how the specification says that a signal stays here, and the tests of this
// package use it, because a test has no collector.
func wanted(of signal) (bool, error) {
	value := strings.TrimSpace(os.Getenv("OTEL_" + string(of) + "_EXPORTER"))

	switch value {
	case "", exporterOTLP:
		return true, nil
	case exporterNone:
		return false, nil
	default:
		return false, fmt.Errorf(
			"OTEL_%s_EXPORTER is %q, and this controller exports through OTLP only: "+
				"the values are %q and %q", of, value, exporterOTLP, exporterNone)
	}
}

// protocol gives the OTLP transport of a signal. The specification gives the signal its own
// variable, and a general variable behind it.
func protocol(of signal) (string, error) {
	value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_" + string(of) + "_PROTOCOL"))
	if value == "" {
		value = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
	}

	switch value {
	// The default of the specification is HTTP with protobuf.
	case "":
		return protocolHTTP, nil
	case protocolGRPC, protocolHTTP:
		return value, nil
	default:
		return "", fmt.Errorf("the OTLP protocol %q is not one that this controller has: "+
			"the values are %q and %q", value, protocolGRPC, protocolHTTP)
	}
}

// spanExporter gives the exporter of the spans, or nothing when the spans stay here.
func spanExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	export, err := wanted(traces)
	if err != nil || !export {
		return nil, err
	}

	chosen, err := protocol(traces)
	if err != nil {
		return nil, err
	}

	if chosen == protocolGRPC {
		return otlptracegrpc.New(ctx)
	}

	return otlptracehttp.New(ctx)
}

// metricReader gives the reader that sends the metrics, or nothing when they stay here.
func metricReader(ctx context.Context) (sdkmetric.Reader, error) {
	export, err := wanted(metrics)
	if err != nil || !export {
		return nil, err
	}

	chosen, err := protocol(metrics)
	if err != nil {
		return nil, err
	}

	var exporter sdkmetric.Exporter

	if chosen == protocolGRPC {
		exporter, err = otlpmetricgrpc.New(ctx)
	} else {
		exporter, err = otlpmetrichttp.New(ctx)
	}

	if err != nil {
		return nil, err
	}

	return sdkmetric.NewPeriodicReader(exporter), nil
}

// logExporter gives the exporter of the log records, or nothing when they stay here. The records
// still reach stdout, which is the other destination of every record.
func logExporter(ctx context.Context) (sdklog.Exporter, error) {
	export, err := wanted(logs)
	if err != nil || !export {
		return nil, err
	}

	chosen, err := protocol(logs)
	if err != nil {
		return nil, err
	}

	if chosen == protocolGRPC {
		return otlploggrpc.New(ctx)
	}

	return otlploghttp.New(ctx)
}
