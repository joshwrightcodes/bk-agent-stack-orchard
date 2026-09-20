// Package job holds the identity of a Buildkite job.
//
// A job identifier travels through most of the controller: the scheduled-jobs list, a
// reservation, a job acquisition token, the name of a virtual machine, and a job state. A plain
// string in each of those places invites a mistake that the compiler cannot see, such as a
// profile name or a virtual machine name in the place of a job. ID is that guard.
package job

import (
	"fmt"
	"regexp"
)

// idFormat is the shape that Buildkite uses: eight, four, four, four, and twelve hexadecimal
// digits. The format holds for every UUID version, so the controller needs no version of its
// own.
var idFormat = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ID is the identifier of one Buildkite job.
//
// The identifier keeps its text form, because every system that the controller talks to uses
// that form: Buildkite in its API, and Orchard in the name of a virtual machine.
type ID string

// ParseID reads an identifier that Buildkite gave. It is the only way to make an ID from text
// that the controller did not create, so an identifier that reaches the rest of the controller
// has a known shape.
func ParseID(text string) (ID, error) {
	if !idFormat.MatchString(text) {
		return "", fmt.Errorf("%q is not a Buildkite job identifier", text)
	}

	return ID(text), nil
}

// Valid reports whether the identifier has the shape of a Buildkite job identifier.
func (id ID) Valid() bool {
	return idFormat.MatchString(string(id))
}

// String gives the text form, which is what Buildkite and Orchard both use.
func (id ID) String() string {
	return string(id)
}
