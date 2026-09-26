// Command bk-agent-stack-orchard runs Buildkite macOS jobs in temporary Tart virtual machines
// through Orchard.
//
// The process reads its configuration, starts telemetry, builds the clients of Buildkite and of
// Orchard, and runs one control loop until a signal stops it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

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

	path := flags.String("config", "", "the path of the configuration file")
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

	if *path == "" {
		_, _ = fmt.Fprintln(problems, "bk-agent-stack-orchard: -config is necessary")

		return 2
	}

	// A signal stops the loop, and the loop then withdraws the stack and stops the exporters.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, *path, out); err != nil {
		_, _ = fmt.Fprintf(problems, "bk-agent-stack-orchard: %v\n", err)

		return 1
	}

	return 0
}

// serve runs the controller until the context ends.
func serve(ctx context.Context, path string, out io.Writer) error {
	settings, err := config.Load(path, os.Getenv)
	if err != nil {
		return err
	}

	recorder, err := telemetry.Setup(ctx, telemetry.Options{
		Version:      version,
		ControllerID: settings.ControllerID,
		Output:       out,
	})
	if err != nil {
		return err
	}

	// The records of the last seconds of a process are the interesting ones, so telemetry stops
	// after everything that writes to it.
	defer func() {
		if err := recorder.Shutdown(context.WithoutCancel(ctx)); err != nil {
			_, _ = fmt.Fprintf(os.Stderr,
				"bk-agent-stack-orchard: stopping telemetry: %v\n", err)
		}
	}()

	for _, warning := range settings.Warnings {
		recorder.Logger.WarnContext(ctx, warning)
	}

	// The calls of this controller become spans and metrics of the HTTP conventions, which is
	// where an operator looks first when a call is slow or fails.
	client := &http.Client{Transport: otelhttp.NewTransport(nil)}

	stacks, err := buildkite.New(buildkite.Options{
		Token:      settings.Buildkite.AgentToken,
		Endpoint:   settings.Buildkite.Endpoint,
		StackKey:   settings.ControllerID,
		Queue:      settings.Buildkite.Queue,
		Logger:     recorder.Logger,
		HTTPClient: client,
	})
	if err != nil {
		return err
	}

	machines, err := orchard.New(orchard.Options{
		URL:            settings.Orchard.URL,
		ServiceAccount: settings.Orchard.ServiceAccount,
		Token:          settings.Orchard.Token,
		HTTPClient:     client,
	})
	if err != nil {
		return err
	}

	now := time.Now //nolint:forbidigo // the binary runs on the clock of the machine

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
		Now:          now,
		Logger:       recorder.Logger,
	})
	if err != nil {
		return err
	}

	stopProbes, err := probe(ctx, settings.HealthAddress, checks)
	if err != nil {
		return err
	}
	defer stopProbes()

	return loop.Run(ctx)
}

// probe serves the gRPC health service of the probes, and keeps it current, until the returned
// function stops both. A port that another process holds fails the start, because a controller
// whose probes nobody can reach looks dead to the platform, which then restarts it for ever.
func probe(ctx context.Context, address string, checks *health.Checks) (func(), error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("serving the probes on %s: %w", address, err)
	}

	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, checks.Server())

	go func() { _ = server.Serve(listener) }()

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
func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}
