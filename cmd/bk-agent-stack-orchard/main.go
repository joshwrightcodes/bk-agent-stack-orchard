// SPDX-License-Identifier: AGPL-3.0-or-later

// Command bk-agent-stack-orchard runs Buildkite macOS jobs in temporary Tart virtual machines
// through Orchard.
//
// The process reads its configuration, starts telemetry, builds the clients of Buildkite and of
// Orchard, and runs one control loop until a signal stops it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/app"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/health"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// The release build sets these through ldflags. A development build leaves them empty.
var (
	version string
	commit  string
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main with its inputs and its outputs given, so that a test can call it.
func run(args []string, out, problems io.Writer) int {
	flags := flag.NewFlagSet("bk-agent-stack-orchard", flag.ContinueOnError)
	flags.SetOutput(problems)

	path := flags.String("config", "",
		"the path of the configuration file ("+configEnvironment+" and "+defaultConfigPath+" follow)")
	wanted := flags.Bool("version", false, "print the version and stop")

	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *wanted {
		if _, err := fmt.Fprintf(out, "bk-agent-stack-orchard %s (%s)\n",
			orUnknown(version), orUnknown(commit)); err != nil {
			_, _ = fmt.Fprintf(problems,
				"bk-agent-stack-orchard: cannot write the version: %v\n", err)

			return 1
		}

		return 0
	}

	location, origin := configPath(*path, os.Getenv)

	// A signal stops the loop, and the loop then withdraws the stack and stops the exporters.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, location, origin, out, problems); err != nil {
		_, _ = fmt.Fprintf(problems, "bk-agent-stack-orchard: %v\n", err)

		return 1
	}

	return 0
}

// The configuration path comes from the flag first, then from the environment, then from this
// default, which is where the Helm chart mounts the file.
const (
	configEnvironment = "BK_AGENT_STACK_ORCHARD_CONFIG"
	defaultConfigPath = "/etc/bk-agent-stack-orchard/config.yaml"
)

// configPath gives the path of the configuration file and where the path came from: the flag when
// it is set, else the environment variable, else the default.
func configPath(flagged string, getenv func(string) string) (path, origin string) {
	if flagged != "" {
		return flagged, "the -config flag"
	}

	if fromEnvironment := getenv(configEnvironment); fromEnvironment != "" {
		return fromEnvironment, "the environment variable " + configEnvironment
	}

	return defaultConfigPath, "the default path"
}

// serve runs the controller until the context ends. A failure after the start of telemetry that
// no log record can carry goes to problems.
func serve(ctx context.Context, path, origin string, out, problems io.Writer) error {
	settings, err := config.Load(path, os.Getenv)
	if err != nil {
		return fmt.Errorf("%w (the path %s comes from %s)", err, path, origin)
	}

	recorder, err := telemetry.Setup(ctx, telemetry.Options{
		Version:      serviceVersion(version, debug.ReadBuildInfo),
		ControllerID: settings.ControllerID,
		Output:       out,
	})
	if err != nil {
		return err
	}

	// The records of the last seconds of a process are the interesting ones, so telemetry stops
	// after everything that writes to it.
	defer stopTelemetry(ctx, recorder.Shutdown, problems)

	for _, warning := range settings.Warnings {
		recorder.Logger.WarnContext(ctx, warning)
	}

	now := time.Now //nolint:forbidigo // the binary runs on the clock of the machine

	// The calls of this controller become spans and metrics of the HTTP conventions, which is
	// where an operator looks first when a call is slow or fails.
	client := newHTTPClient(nil)

	stacks, err := buildkite.New(buildkite.Options{
		Token:      settings.Buildkite.AgentToken,
		Endpoint:   settings.Buildkite.Endpoint,
		StackKey:   settings.ControllerID,
		Queue:      settings.Buildkite.Queue,
		Logger:     recorder.Logger,
		HTTPClient: client,
		Now:        now,
	})
	if err != nil {
		return err
	}

	// Orchard makes its own certificate, and the calls to it trust the one that the operator
	// gives. The calls to Buildkite keep the authorities of the system.
	toOrchard, err := orchard.Transport(settings.Orchard.CACert)
	if err != nil {
		return err
	}

	machines, err := orchard.New(orchard.Options{
		URL:            settings.Orchard.URL,
		ServiceAccount: settings.Orchard.ServiceAccount,
		Token:          settings.Orchard.Token,
		HTTPClient:     newHTTPClient(toOrchard),
		Now:            now,
	})
	if err != nil {
		return err
	}

	loop, err := app.Build(app.Options{
		Config:      settings,
		Telemetry:   recorder,
		Stacks:      stacks,
		Orchard:     machines,
		Now:         now,
		Environment: os.Getenv,
	})
	if err != nil {
		return err
	}

	// Readiness rests on every source of the loop, so a source that the loop gains is part of it.
	var sources []health.Source
	for _, source := range loop.Sources() {
		sources = append(sources, source)
	}

	checks, err := health.New(health.Options{
		Sources:      sources,
		Unauthorized: stacks.Unauthorized,
		Window:       settings.StateFreshness,
		Heartbeat:    loop.Heartbeat,
		Stall:        livenessStall,
		Now:          now,
		Logger:       recorder.Logger,
	})
	if err != nil {
		return err
	}

	stopProbes, err := probe(ctx, settings.HealthAddress, checks, recorder.Logger)
	if err != nil {
		return err
	}
	defer stopProbes()

	return loop.Run(ctx)
}

// livenessStall is how long the loop may go without progress before liveness fails and the
// platform restarts the process. The loop makes progress when it starts and when a tick ends, so
// the bound covers one whole tick. Each call to an API has a limit of 10 s, and the longest tick,
// in which every call takes its whole limit, is 230 s (target design, section 4), so the bound is
// more than three times that tick. A restart cures a loop that waits without an end, and not an
// outage.
const livenessStall = 15 * time.Minute

// telemetryGrace is how long the stop of telemetry may take. An exporter that cannot reach the
// collector retries for about a minute, and the platform kills a process that outlives its
// termination grace, so the stop gives up first. The chart gives the pod a termination grace of
// 45 s, which covers the withdrawal of the stack (10 s) and this grace.
const telemetryGrace = 10 * time.Second

// stopTelemetry stops telemetry inside its grace. A signal has cancelled the context of the
// process by then, so the stop gets a context of its own. No log record can carry its failure,
// so the failure goes to problems.
func stopTelemetry(ctx context.Context, shutdown func(context.Context) error, problems io.Writer) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryGrace)
	defer cancel()

	if err := shutdown(ctx); err != nil {
		_, _ = fmt.Fprintf(problems, "bk-agent-stack-orchard: stopping telemetry: %v\n", err)
	}
}

// probeStreams is the most streams that one connection to the probe server may hold. A probe of
// the kubelet is one call on a new connection, and each Watch is a stream without an end, so a
// low limit costs a probe nothing and stops one client from holding many streams.
const probeStreams = 16

// newProbeServer gives the gRPC server of the probes. Its limits protect a pod with a small
// memory limit from a client that holds streams or connections open, or that pings without end.
func newProbeServer() *grpc.Server {
	return grpc.NewServer(
		grpc.MaxConcurrentStreams(probeStreams),
		// A health request names one service, so a large message is never a probe.
		grpc.MaxRecvMsgSize(4096),
		// A client may ping once a minute, and only while it has a stream. The server closes a
		// connection that a client pings more often.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: time.Minute}),
		// A connection with no stream for 5 minutes closes.
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: 5 * time.Minute}),
	)
}

// serveProbes serves the probes until the server stops. A server that stops for another reason
// leaves a controller whose probes nobody can reach, so the log says why.
func serveProbes(ctx context.Context, server *grpc.Server, listener net.Listener,
	logger *slog.Logger,
) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		logger.ErrorContext(ctx, "the probe server stopped, so the platform cannot probe the "+
			"controller", "error", err.Error())
	}
}

// probe serves the gRPC health service of the probes, and keeps it current, until the returned
// function stops both. A port that another process holds fails the start, because a controller
// whose probes nobody can reach looks dead to the platform, which then restarts it for ever.
func probe(ctx context.Context, address string, checks *health.Checks, logger *slog.Logger,
) (func(), error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("serving the probes on %s: %w", address, err)
	}

	server := newProbeServer()
	healthpb.RegisterHealthServer(server, checks.Server())

	go serveProbes(ctx, server, listener, logger)

	updates, stop := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		checks.Run(updates, time.Second)
	}()

	return func() {
		stop()
		<-done

		// A Watch of a probe is a stream without an end, so the stop does not wait for it.
		server.Stop()
	}, nil
}

// orUnknown gives a value that a development build left empty a name that a person can read.
// serviceVersion gives the version that the telemetry of this process carries: the version of the
// release build, or else the version that Go stamped from the module and its commit, such as the
// pseudo-version of a ko build. A build of neither, such as go run, gives empty text, and the
// telemetry then has no version.
func serviceVersion(released string, read func() (*debug.BuildInfo, bool)) string {
	if released != "" {
		return released
	}

	info, ok := read()
	if !ok || info.Main.Version == "(devel)" {
		return ""
	}

	return info.Main.Version
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}

// httpTimeout limits one HTTP request to Buildkite or to Orchard. The loop gives each call a
// shorter limit of its own, which covers all of its tries (internal/app), and this limit holds
// for a request outside a call of the loop too.
const httpTimeout = 30 * time.Second

// newHTTPClient builds the client for one remote service. A nil transport gives the default one.
func newHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: otelhttp.NewTransport(transport), Timeout: httpTimeout}
}
