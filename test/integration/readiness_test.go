//go:build integration

package integration_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// T-063. An owner revokes the cluster agent token while the controller runs. Every call to the
// Stacks API then gets HTTP 401, the readiness service stops serving, and the log names the
// token. The liveness service still serves: a restart cannot cure a revoked token.
func TestARevokedTokenMakesTheControllerUnready(t *testing.T) {
	startOrchard(t, orchardBinary(t))

	stacks := stacksWith(t)
	config, probes := writeConfigWithProbes(t, stacks.URL())
	process := startController(t, config)

	connection, err := grpc.NewClient(probes,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("connecting to the probes at %s: %v", probes, err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	probe := healthpb.NewHealthClient(connection)

	eventually(t, "the controller is ready", func() bool {
		return status(t, probe, "readiness") == healthpb.HealthCheckResponse_SERVING
	})

	stacks.Revoke()

	eventually(t, "the controller is not ready", func() bool {
		return status(t, probe, "readiness") == healthpb.HealthCheckResponse_NOT_SERVING
	})

	// The protocol carries no reason, so the log does.
	eventually(t, "the log names the token", func() bool {
		written, err := os.ReadFile(process.log)

		return err == nil && strings.Contains(string(written), "refused the agent token")
	})

	if got := status(t, probe, "liveness"); got != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("liveness is %s, want SERVING while the token is revoked", got)
	}
}

// status gives what a probe of one service reads, or UNKNOWN while nothing answers.
func status(t *testing.T, probe healthpb.HealthClient, service string,
) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	answer, err := probe.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		return healthpb.HealthCheckResponse_UNKNOWN
	}

	return answer.GetStatus()
}
