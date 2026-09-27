#!/usr/bin/env bash
# Stops the processes that an earlier run of the integration tests left on the runner.
#
# When GitHub cancels a job, the runner stops the processes of the step: mise, go test, and the
# test binary. The test binary starts `orchard dev` and the controller, and a process that the
# runner kills runs no cleanup, so both continue as orphans. The next run then finds port 6120
# in use, and every test fails. CI runs this before the tests and after them, even when the run is
# cancelled. It stops only what the tests start, but it stops each one on the machine, so run it
# on a CI runner, not on a machine where you work.
set -euo pipefail

port=6120

# An `orchard dev` of a test, and a controller that TestMain built into a temp dir.
patterns=(
	"orchard dev "
	"bk-agent-stack-orchard-integration[0-9]+/bk-agent-stack-orchard "
)

found=0
for pattern in "${patterns[@]}"; do
	if pgrep -f "$pattern" >/dev/null; then
		found=1
		echo "stop-leftovers: stopping $(pgrep -f "$pattern" | tr '\n' ' ')(${pattern% })"
		pkill -TERM -f "$pattern" || true
	fi
done

# Each process has 10 seconds to stop, as the tests give it. Then it stops without a choice.
if ((found)); then
	for _ in $(seq 1 10); do
		still=0
		for pattern in "${patterns[@]}"; do
			pgrep -f "$pattern" >/dev/null && still=1
		done
		((still)) || break
		sleep 1
	done
	for pattern in "${patterns[@]}"; do
		pkill -KILL -f "$pattern" 2>/dev/null || true
	done
fi

# Something else on the port is not a leftover of this repository, so the script does not stop
# it. It fails here, with the name of the process, instead of in each test.
for _ in $(seq 1 10); do
	if ! nc -z -G 1 127.0.0.1 "$port" 2>/dev/null; then
		echo "stop-leftovers: port $port is free"
		exit 0
	fi
	sleep 1
done

echo "stop-leftovers: port $port is still in use: $(lsof -nP -iTCP:$port -sTCP:LISTEN 2>/dev/null | tail -n +2)" >&2
exit 1
