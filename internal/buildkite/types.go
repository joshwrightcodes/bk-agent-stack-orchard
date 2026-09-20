package buildkite

import (
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// ScheduledJob is one job that waits in the queue.
//
// The list gives every job of the queue, because the Stacks API filters by queue only. The
// Matcher reads Rules to find the profile of the job.
type ScheduledJob struct {
	ID job.ID

	// Priority orders the jobs. A higher number goes first.
	Priority int

	// Rules are the agent tags that the step asks for, as "key=value" text.
	Rules []string

	// RunnableAt is when the job became able to run.
	RunnableAt time.Time

	// Pipeline and Build name the job for a person who reads a log line.
	Pipeline string
	Build    int
}

// Page is one answer of the scheduled-jobs list.
type Page struct {
	Jobs []ScheduledJob

	// Paused reports a queue whose dispatch an operator stopped. The controller then starts no
	// VM, and the VMs in progress continue.
	Paused bool

	// NextCursor asks for the next page. It is empty on the last page.
	NextCursor string
}

// Reservation is the answer of a batch reserve. The call holds a job for one stack, and a job
// that another stack holds is in NotReserved. A partial answer is normal, and the caller
// continues with the reserved jobs only.
type Reservation struct {
	Reserved    []job.ID
	NotReserved []job.ID
}

// Token is one job acquisition token, which lets one agent acquire one job.
type Token struct {
	JobID job.ID

	// Token goes into the VM of the job and nowhere else. Its type keeps it out of a log.
	Token secret.String

	// ExpiresAt is when the token stops working. The reservation of the job can end it earlier.
	ExpiresAt time.Time
}

// Tokens is the answer of a token call. A job in NotIssued gets no VM, and its reservation
// expires.
type Tokens struct {
	Issued    []Token
	NotIssued []job.ID
}

// Notification is one line on the job page of Buildkite.
type Notification struct {
	JobID  job.ID
	Detail string

	// At is when the event happened. A zero time means now.
	At time.Time
}
