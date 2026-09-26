package reaper_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/inventory"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
)

// starting gives machines that run, whose reserved jobs no agent has acquired yet, inside every
// timeout: row 13.
func starting(ids ...job.ID) []inventory.Owned {
	machines := make([]inventory.Owned, 0, len(ids))
	for _, id := range ids {
		machine := owned(id, time.Minute)
		machine.VM.StartedAt = now.Add(-time.Minute)
		machines = append(machines, machine)
	}

	return machines
}

func reservedStates(ids ...job.ID) map[job.ID]string {
	states := map[job.ID]string{}
	for _, id := range ids {
		states[id] = "reserved"
	}

	return states
}

// T-025. Every extension of a pass goes in one batch-reserve call, because one call for each
// machine would spend the rate limit of the organisation on a busy fleet.
func TestEveryExtensionOfAPassGoesInOneCall(t *testing.T) {
	t.Parallel()

	ids := []job.ID{ended, running, ancient}
	reservations := &held{until: map[job.ID]time.Time{
		ended: now.Add(time.Minute), running: now.Add(time.Minute), ancient: now.Add(time.Minute),
	}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{Owned: starting(ids...)}}

	reaping := buildHolding(t, &observed{states: reservedStates(ids...)}, machines, &decisions{},
		&pages{}, reservations)
	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(reservations.calls) != 1 {
		t.Fatalf("the reaper made %d extension calls, want 1 for the whole pass",
			len(reservations.calls))
	}

	got := slices.Clone(reservations.calls[0])
	slices.Sort(got)

	want := slices.Clone(ids)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("the call extended %v, want all of %v", got, want)
	}

	if len(machines.deleted) != 0 {
		t.Errorf("orchard deleted %v, want nothing: these machines are starting", machines.deleted)
	}
}

// T-025. The renewal starts once less than reservationRenewBefore is left, and not before, so a
// reservation is never extended on every tick.
func TestTheRenewalStartsInsideTheLeadAndNotBefore(t *testing.T) {
	t.Parallel()

	reservations := &held{until: map[job.ID]time.Time{
		ended:   now.Add(100 * time.Second),
		running: now.Add(200 * time.Second),
	}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{Owned: starting(ended, running)}}

	reaping := buildHolding(t, &observed{states: reservedStates(ended, running)}, machines,
		&decisions{}, &pages{}, reservations)
	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(reservations.calls) != 1 || !slices.Equal(reservations.calls[0], []job.ID{ended}) {
		t.Errorf("the reaper extended %v, want only the reservation with 100s left",
			reservations.calls)
	}
}

// After a restart the controller knows the end of no reservation, so it extends every one at once
// rather than guess.
func TestAReservationOfUnknownEndIsExtendedAtOnce(t *testing.T) {
	t.Parallel()

	reservations := &held{until: map[job.ID]time.Time{}}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{Owned: starting(ended)}}

	reaping := buildHolding(t, &observed{states: reservedStates(ended)}, machines, &decisions{},
		&pages{}, reservations)
	if err := reaping.Reap(t.Context()); err != nil {
		t.Fatalf("Reap() = %v, want no error", err)
	}

	if len(reservations.calls) != 1 {
		t.Errorf("the reaper made %d extension calls, want one for the reservation of unknown end",
			len(reservations.calls))
	}
}

// An extension that fails reaches the tick, and the other actions of the pass still happen.
func TestAFailedExtensionDoesNotStopThePass(t *testing.T) {
	t.Parallel()

	reservations := &held{
		until:     map[job.ID]time.Time{running: now.Add(time.Minute)},
		extendErr: errors.New("buildkite is not available"),
	}
	machines := &fleet{read: true, snapshot: inventory.Snapshot{
		Owned: append(starting(running), owned(ended, time.Hour)),
	}}
	api := &observed{states: map[job.ID]string{running: "reserved", ended: "finished"}}

	err := buildHolding(t, api, machines, &decisions{}, &pages{}, reservations).Reap(t.Context())
	if err == nil {
		t.Error("Reap() = nil, want the failed extension to reach the tick")
	}

	if len(machines.deleted) != 1 {
		t.Errorf("orchard deleted %v, want the machine of the finished job all the same",
			machines.deleted)
	}
}
