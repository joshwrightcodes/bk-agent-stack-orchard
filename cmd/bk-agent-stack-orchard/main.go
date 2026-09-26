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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/admission"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/buildkite"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/config"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/controller"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/jobstates"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/launcher"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/reaper"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/stack"
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

	loop, err := assemble(settings, recorder)
	if err != nil {
		return err
	}

	return loop.Run(ctx)
}

// assemble builds the parts of the controller and wires them together.
func assemble(settings *config.Config, recorder *telemetry.Telemetry) (*controller.Controller, error) {
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
		return nil, err
	}

	registration, err := stack.New(stacks)
	if err != nil {
		return nil, err
	}

	poller, err := stack.NewPoller(stack.PollerOptions{
		Stacks:   stacks,
		Interval: settings.Buildkite.PollInterval,
	})
	if err != nil {
		return nil, err
	}

	machines, err := orchard.New(orchard.Options{
		URL:            settings.Orchard.URL,
		ServiceAccount: settings.Orchard.ServiceAccount,
		Token:          settings.Orchard.Token,
		HTTPClient:     client,
	})
	if err != nil {
		return nil, err
	}

	fleet, err := inventory.New(inventory.Options{
		Orchard:        machines,
		ControllerID:   settings.ControllerID,
		Interval:       settings.Orchard.InventoryInterval,
		OfflineTimeout: settings.Orchard.WorkerOfflineTimeout,
	})
	if err != nil {
		return nil, err
	}

	starter, err := launcher.New(launcher.Options{
		Stacks:            stacks,
		Orchard:           machines,
		ControllerID:      settings.ControllerID,
		Queue:             settings.Buildkite.Queue,
		ReservationExpiry: settings.Buildkite.ReservationExpiry,
		TokenLifetime:     settings.Buildkite.JobTokenLifetime,
		Environment:       os.Getenv,
		Metrics:           recorder.Metrics,
		Logger:            recorder.Logger,
		Tracer:            recorder.Tracer,
	})
	if err != nil {
		return nil, err
	}

	// The job states ask about the machines that the inventory holds, so they follow it in the
	// sources of the loop, and each tick asks about the machines that it has just read.
	observed, err := jobstates.New(jobstates.Options{
		Stacks:   stacks,
		Fleet:    fleet,
		Interval: settings.Buildkite.JobStatesInterval,
	})
	if err != nil {
		return nil, err
	}

	reaping, err := reaper.New(reaper.Options{
		Stacks:    stacks,
		States:    observed,
		Orchard:   machines,
		Fleet:     fleet,
		Profile:   starter.Profile,
		Profiles:  settings.Profiles,
		Defaults:  settings.Defaults.Timeouts,
		Freshness: settings.StateFreshness,
		Metrics:   recorder.Metrics,
		Logger:    recorder.Logger,
		Tracer:    recorder.Tracer,
	})
	if err != nil {
		return nil, err
	}

	decisions := &admitter{
		settings: settings,
		queue:    poller,
		fleet:    fleet,
		starter:  starter,
		logger:   recorder.Logger,
		metrics:  recorder.Metrics,
	}

	return controller.New(controller.Options{
		Stack:    registration,
		Sources:  []controller.Source{poller, fleet, observed},
		Queue:    poller,
		Fleet:    decisions.observe,
		Reap:     reaping.Reap,
		Admit:    decisions.admit,
		Interval: settings.Buildkite.PollInterval,
		Metrics:  recorder.Metrics,
		Logger:   recorder.Logger,
		Tracer:   recorder.Tracer,
	})
}

// admitter runs the admission decision of a tick and remembers what it decided, so that the
// observation of the same tick can report it.
type admitter struct {
	settings *config.Config
	queue    *stack.Poller
	fleet    *inventory.Inventory
	starter  *launcher.Launcher
	logger   *slog.Logger
	metrics  *telemetry.Metrics

	mu     sync.Mutex
	latest admission.Result
}

// admit decides which jobs the fleet can carry, and starts them.
func (a *admitter) admit(ctx context.Context) error {
	queue, read := a.queue.Latest()
	if !read {
		return nil
	}

	held, read := a.fleet.Latest()
	if !read {
		// Admission needs the machines as well as the queue: without them it would count the
		// room of the fleet as empty and start a job for every waiting one.
		return nil
	}

	result := admission.Decide(admission.Request{
		Jobs:        queue.Jobs,
		Profiles:    a.settings.Profiles,
		Fleet:       held,
		Held:        a.starter.Held(held),
		Eligible:    a.fleet.Eligible,
		MaxInFlight: a.settings.MaxInFlight,
		Now:         time.Now(),
	})

	for _, unmatched := range result.Unmatched {
		a.metrics.Unmatched(ctx)
		a.logger.WarnContext(ctx, "no profile carries this job",
			telemetry.LogJob, string(unmatched.Job.ID),
			telemetry.LogPipeline, unmatched.Job.Pipeline,
			"reason", unmatched.Reason)
	}

	a.mu.Lock()
	a.latest = result
	a.mu.Unlock()

	return a.starter.Launch(ctx, result, held)
}

// observe gives the instruments what this tick saw.
func (a *admitter) observe() telemetry.Observation {
	a.mu.Lock()
	result := a.latest
	a.mu.Unlock()

	observation := telemetry.Observation{}
	result.Observe(&observation)

	if held, read := a.fleet.Latest(); read {
		held.Observe(&observation, a.fleet.Online, a.profileOf)
	}

	return observation
}

// profileOf gives the profile of a job that this process started. A machine of a process before
// a restart has none, and it counts as one of an unknown profile until it ends.
func (a *admitter) profileOf(id job.ID) string {
	return a.starter.Profile(id)
}

// orUnknown gives a value that a development build left empty a name that a person can read.
func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}
