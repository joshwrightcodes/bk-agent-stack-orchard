package reaper

import (
	"fmt"
)

// message gives the line that a job page shows when the controller ends a job or reports on its
// machine. It says what happened in the words of the pipeline author, and the status of the
// machine as Orchard gave it.
func message(machine Machine, decision Decision) string {
	switch decision.Reason {
	case ReasonVMFailed:
		return withStatus("the virtual machine of this job failed", machine)
	case ReasonBootTimeout:
		return withStatus(fmt.Sprintf("the virtual machine of this job did not start within %s",
			machine.Timeouts.Boot), machine)
	case ReasonAcquireTimeout:
		return fmt.Sprintf("no agent acquired this job within %s of the start of its virtual "+
			"machine", machine.Timeouts.Acquire)
	default:
		return fmt.Sprintf("the controller removed the virtual machine of this job: %s",
			decision.Reason)
	}
}

// withStatus adds the status message of the machine, when Orchard gave one.
func withStatus(text string, machine Machine) string {
	if machine.VM.StatusMessage == "" {
		return text
	}

	return text + ": " + machine.VM.StatusMessage
}
