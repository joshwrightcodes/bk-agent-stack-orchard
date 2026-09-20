package orchard

import (
	"fmt"
	"log/slog"
)

// A specification carries the job acquisition token in its environment, and the image password
// when a profile uses the SSH transport. The worker needs both, and a log line needs neither.
// These methods belong beside the generated types, not in them, because a generator writes the
// shape of the API and this project decides what a log may see.

// LogValue gives a view of a machine that carries no secret.
func (v VM) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", v.Name),
		slog.String("image", v.Image),
		slog.String("status", v.Status),
		slog.String("worker", v.Worker),
		slog.Uint64("cpu", v.CPU),
		slog.Uint64("memoryMiB", v.Memory),
		slog.Bool("netSoftnet", v.NetSoftnet),
		slog.String("restartPolicy", v.RestartPolicy),
		slog.Bool("hasImageCredentials", v.Username != ""),
	)
}

// String hides a machine from every format verb, because it carries the token of a job.
func (v VM) String() string {
	return fmt.Sprintf("VM(%s)", v.Name)
}

// GoString hides a machine from the %#v verb.
func (v VM) GoString() string {
	return fmt.Sprintf("orchard.VM{Name:%q}", v.Name)
}
