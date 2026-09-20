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

// Placed reports a machine that Orchard gave to a worker. The resources of such a machine are
// spent, and the resources of one that waits are not.
//
// Orchard decides the same way, except that it reads a scheduling condition first when the
// machine carries one. This client does not read the conditions of a machine, so it uses the
// answer that Orchard falls back to. The difference can make this accounting believe that a
// worker has room for one more machine than it has, and the cost of that is one machine that
// waits a little longer: Orchard makes the real placement, and this accounting only decides how
// many jobs to start.
func (v VM) Placed() bool {
	return v.Worker != ""
}

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
