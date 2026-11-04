// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 The bk-agent-stack-orchard Authors

package orchard

// The resource names of Orchard. A worker advertises what it has under these names, and a
// machine asks for what it needs under the same ones.
//
// The names carry the domain of the upstream project, because Orchard chose them. A short name
// such as "tart-vms" is what the documents of this project write, and it is not what the API
// carries.
const (
	// ResourceVMs is the number of machines that a worker can run at one time.
	ResourceVMs = "org.cirruslabs.tart-vms"

	// ResourceCores is the number of logical cores.
	ResourceCores = "org.cirruslabs.logical-cores"

	// ResourceMemoryMiB is the memory in mebibytes.
	ResourceMemoryMiB = "org.cirruslabs.memory-mib"
)

// Placed reports a machine that Orchard has scheduled. The resources of such a machine are
// spent, and the resources of one that waits are not.
//
// Orchard decides in the same way. When the machine carries a scheduled condition, the state of
// that condition is the answer. Only when the machine carries none does the worker name decide.
// The difference matters because Orchard keeps the worker name of a machine that failed or
// stopped, and sets its scheduled condition to false to free the resources. Orchard 0.58.0 does
// this in IsScheduled of pkg/resource/v1/v1.go, and in the health check of its scheduler.
func (v VM) Placed() bool {
	for _, condition := range v.Conditions {
		if condition.Type == conditionScheduled {
			return condition.State == conditionTrue
		}
	}

	return v.Worker != ""
}

// The scheduled condition of Orchard, and the state that means true.
const (
	conditionScheduled = "scheduled"
	conditionTrue      = "true"
)

// Free gives what a worker has left, after the machines that it already carries.
//
// A subtraction stops at zero, as it does in Orchard, because a worker whose machines ask for
// more than it advertises has nothing left and not a negative amount.
func Free(worker Worker, carried []VM) map[string]uint64 {
	free := make(map[string]uint64, len(worker.Resources))
	for name, value := range worker.Resources {
		free[name] = value
	}

	for _, vm := range carried {
		for name, value := range vm.Resources {
			if value >= free[name] {
				free[name] = 0

				continue
			}

			free[name] -= value
		}
	}

	return free
}

// Fits reports resources that can carry a request. A resource that the free set does not name
// counts as zero, so a machine that asks for something that a worker never advertised does not
// fit.
func Fits(free, request map[string]uint64) bool {
	for name, value := range request {
		if value > free[name] {
			return false
		}
	}

	return true
}

// Take subtracts a request from a free set, in place.
func Take(free, request map[string]uint64) {
	for name, value := range request {
		if value >= free[name] {
			free[name] = 0

			continue
		}

		free[name] -= value
	}
}
