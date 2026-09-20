package telemetry_test

import (
	"context"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/telemetry"
)

// observation is a fleet that uses every series once.
func observation() telemetry.Observation {
	return telemetry.Observation{
		Waiting:       map[string]int64{"xcode-27": 4},
		OldestWaiting: map[string]time.Duration{"xcode-27": 90 * time.Second},
		Slots:         map[telemetry.Slot]int64{{Pool: "pilot", State: telemetry.SlotsFree}: 3},
		VMs:           map[telemetry.VM]int64{{Profile: "xcode-27", Phase: telemetry.VMRunning}: 2},
		Workers:       map[telemetry.WorkerState]int64{telemetry.WorkerBusy: 2},
		Synced:        map[telemetry.Source]time.Time{telemetry.SourceInventory: time.Now()},
	}
}

// recordAll uses every instrument once, and gives what the reader then holds.
func recordAll(t *testing.T) []metricdata.Metrics {
	t.Helper()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))

	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() = %v, want no error", err)
		}
	})

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	instruments.Observe(observation())

	ctx := t.Context()
	instruments.Unmatched(ctx)
	instruments.Failed(ctx, "no_profile")
	instruments.Deleted(ctx, "job_ended")
	instruments.SystemError(ctx, telemetry.ComponentBuildkite, "rate_limited")
	instruments.JobPhase(ctx, "xcode-27", telemetry.PhaseTotal, 4*time.Minute)
	instruments.Tick(ctx, "reap", 300*time.Millisecond)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	if len(collected.ScopeMetrics) != 1 {
		t.Fatalf("the reader holds %d scopes, want 1", len(collected.ScopeMetrics))
	}

	return collected.ScopeMetrics[0].Metrics
}

// points gives the data points of one instrument, whatever its shape.
func points(t *testing.T, recorded metricdata.Metrics) []attribute.Set {
	t.Helper()

	var sets []attribute.Set

	switch data := recorded.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, point := range data.DataPoints {
			sets = append(sets, point.Attributes)
		}
	case metricdata.Gauge[float64]:
		for _, point := range data.DataPoints {
			sets = append(sets, point.Attributes)
		}
	case metricdata.Sum[int64]:
		for _, point := range data.DataPoints {
			sets = append(sets, point.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, point := range data.DataPoints {
			sets = append(sets, point.Attributes)
		}
	default:
		t.Fatalf("the instrument %s holds %T, which this test does not read", recorded.Name, data)
	}

	return sets
}

// attributeKeys gives the attributes of one instrument, in order.
func attributeKeys(t *testing.T, recorded metricdata.Metrics) []string {
	t.Helper()

	var keys []string

	for _, set := range points(t, recorded) {
		for _, pair := range set.ToSlice() {
			keys = append(keys, string(pair.Key))
		}
	}

	slices.Sort(keys)

	return slices.Compact(keys)
}

// additive reports an instrument that adds up, which is what the conventions use for a level or
// for a count of events.
func additive(recorded metricdata.Metrics) bool {
	_, ok := recorded.Data.(metricdata.Sum[int64])

	return ok
}

// The instruments of the target design, section 10.1. The alert conditions read them by name and
// by attribute, so a name that moves breaks an alert quietly: the names are part of the contract
// and not a detail. A change here is a change of the design, and it happens in both places.
var design = []struct {
	name       string
	unit       string
	attributes []string
	// sum is true where the conventions use an up-down counter or a counter, and false for a
	// gauge, which is a value that does not add up.
	sum bool
}{
	{
		"bk_agent_stack_orchard.job.waiting.count", "{job}",
		[]string{"bk_agent_stack_orchard.profile"},
		true,
	},
	{
		"bk_agent_stack_orchard.job.waiting.oldest.age", "s",
		[]string{"bk_agent_stack_orchard.profile"},
		false,
	},
	{"bk_agent_stack_orchard.job.unmatched", "{job}", nil, true},
	{"bk_agent_stack_orchard.job.failed", "{job}", []string{"error.type"}, true},
	{
		"bk_agent_stack_orchard.slot.count", "{slot}",
		[]string{"bk_agent_stack_orchard.pool", "bk_agent_stack_orchard.slot.state"},
		true,
	},
	{
		"bk_agent_stack_orchard.vm.count", "{vm}",
		[]string{"bk_agent_stack_orchard.profile", "bk_agent_stack_orchard.vm.phase"},
		true,
	},
	{"cicd.worker.count", "{worker}", []string{"cicd.worker.state"}, true},
	{
		"bk_agent_stack_orchard.job.phase.duration", "s",
		[]string{"bk_agent_stack_orchard.job.phase", "bk_agent_stack_orchard.profile"},
		false,
	},
	{
		"bk_agent_stack_orchard.vm.deletions", "{vm}",
		[]string{"bk_agent_stack_orchard.vm.delete.reason"},
		true,
	},
	{"cicd.system.errors", "{error}", []string{"cicd.system.component", "error.type"}, true},
	{
		"bk_agent_stack_orchard.tick.duration", "s",
		[]string{"bk_agent_stack_orchard.tick.step"},
		false,
	},
	{
		"bk_agent_stack_orchard.source.staleness", "s",
		[]string{"bk_agent_stack_orchard.source"},
		false,
	},
}

func TestTheInstrumentsAreTheOnesOfTheDesign(t *testing.T) {
	t.Parallel()

	recorded := recordAll(t)

	found := map[string]metricdata.Metrics{}
	for _, one := range recorded {
		found[one.Name] = one
	}

	for _, want := range design {
		one, ok := found[want.name]
		if !ok {
			t.Errorf("no instrument is called %s", want.name)

			continue
		}

		if one.Unit != want.unit {
			t.Errorf("%s has the unit %q, want %q", want.name, one.Unit, want.unit)
		}

		if got := attributeKeys(t, one); !slices.Equal(got, want.attributes) {
			t.Errorf("%s carries the attributes %v, want %v", want.name, got, want.attributes)
		}

		// A level or a count of things adds up, and the conventions give those an up-down
		// counter or a counter. A gauge is for a value that does not add up, such as an age.
		if additive(one) != want.sum {
			t.Errorf("%s adds up: %v, want %v", want.name, additive(one), want.sum)
		}

		if one.Description == "" {
			t.Errorf("%s has no description, which a person in a backend reads", want.name)
		}
	}

	if len(recorded) != len(design) {
		t.Errorf("the meter holds %d instruments, want the %d of the design",
			len(recorded), len(design))
	}
}

// The naming rules of the semantic conventions: an attribute that the conventions do not define
// carries the name of the application, so that a word such as "state" or "source" cannot collide
// with a name that OpenTelemetry gives a meaning later.
func TestEveryAttributeIsNamespaced(t *testing.T) {
	t.Parallel()

	// The attributes of the conventions that this controller uses.
	conventions := []string{
		"error.type", "cicd.worker.state", "cicd.system.component",
	}

	for _, one := range recordAll(t) {
		for _, key := range attributeKeys(t, one) {
			if slices.Contains(conventions, key) {
				continue
			}

			if !strings.HasPrefix(key, "bk_agent_stack_orchard.") {
				t.Errorf("%s carries the attribute %q, which is neither an attribute of the "+
					"conventions nor an attribute of this application", one.Name, key)
			}
		}
	}
}

// Invariant I-7. A job identifier in a metric attribute makes one series for each job, which
// costs the backend without answering a question that a person asks of a metric. The trace and
// the log record hold the job.
func TestNoMetricAttributeCarriesAJobOrAVirtualMachine(t *testing.T) {
	t.Parallel()

	uuid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

	for _, one := range recordAll(t) {
		for _, set := range points(t, one) {
			for _, pair := range set.ToSlice() {
				if uuid.MatchString(pair.Value.String()) {
					t.Errorf("%s carries %s=%q, which holds a job identifier",
						one.Name, pair.Key, pair.Value.String())
				}
			}
		}
	}
}

// The invariant holds by construction and not by care: nothing here takes a job identifier, so a
// caller cannot put one in a metric attribute even by mistake.
func TestNothingOfTheInstrumentsAcceptsAJob(t *testing.T) {
	t.Parallel()

	unwanted := reflect.TypeOf(job.ID(""))

	instruments := reflect.TypeOf(&telemetry.Metrics{})
	for i := range instruments.NumMethod() {
		method := instruments.Method(i)

		for p := range method.Type.NumIn() {
			if method.Type.In(p) == unwanted {
				t.Errorf("Metrics.%s takes a job identifier, which invariant I-7 forbids in a "+
					"metric attribute", method.Name)
			}
		}
	}

	// The observation reaches the same attributes, so it needs the same guard.
	fleet := reflect.TypeOf(telemetry.Observation{})
	for i := range fleet.NumField() {
		field := fleet.Field(i)
		if strings.Contains(field.Type.String(), unwanted.String()) {
			t.Errorf("Observation.%s holds a job identifier, which invariant I-7 forbids in a "+
				"metric attribute", field.Name)
		}
	}
}

// A series that leaves the observation must stop. A profile that no longer exists would
// otherwise report its last count for ever, and the fleet would look busier than it is.
func TestASeriesStopsWhenItLeavesTheObservation(t *testing.T) {
	t.Parallel()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))

	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	instruments.Observe(telemetry.Observation{Waiting: map[string]int64{"xcode-27": 4}})
	instruments.Observe(telemetry.Observation{Waiting: map[string]int64{"xcode-26": 1}})

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	sets := points(t, collected.ScopeMetrics[0].Metrics[0])
	if len(sets) != 1 {
		t.Fatalf("the instrument holds %d series, want only the one of the last observation",
			len(sets))
	}

	value, _ := sets[0].Value("bk_agent_stack_orchard.profile")
	if value.AsString() != "xcode-26" {
		t.Errorf("the series is for the profile %q, want xcode-26", value.AsString())
	}
}

// A metric series needs a value for each attribute. A worker with no pool label would otherwise
// give an empty one, which a backend shows as a series with a missing name.
func TestAWorkerWithNoPoolIsInThePoolNone(t *testing.T) {
	t.Parallel()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))

	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	instruments, err := telemetry.NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics() = %v, want the instruments", err)
	}

	instruments.Observe(telemetry.Observation{
		Slots: map[telemetry.Slot]int64{{State: telemetry.SlotsTotal}: 12},
	})

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect() = %v, want the metrics", err)
	}

	value, ok := points(t, collected.ScopeMetrics[0].Metrics[0])[0].
		Value("bk_agent_stack_orchard.pool")
	if !ok || value.AsString() != "none" {
		t.Errorf("the pool attribute is %q, want none", value.AsString())
	}
}
