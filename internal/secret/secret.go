// Package secret holds a value that must not reach a log, a metric, or a virtual machine
// specification.
//
// The type lives in its own package because more than one layer carries a secret: the
// configuration reads them, the Orchard client authenticates with one, and the specification
// builder puts the token of a job into a machine. A package that carries a secret should not
// import the configuration of the controller to get its type.
package secret

import (
	"log/slog"
)

// redacted is what every reader of a secret sees.
const redacted = "[redacted]"

// String is a secret in text form. It prints as "[redacted]" through every path that a value
// usually escapes by: a format verb, a log handler, a JSON encoder, and a text encoder. Reveal
// gives the value, and every call is a place where the secret leaves.
type String string

// String hides the value from every format verb.
func (String) String() string { return redacted }

// GoString hides the value from the %#v verb, which asks GoStringer and never String.
func (String) GoString() string { return `"` + redacted + `"` }

// LogValue hides the value from slog when the secret is an attribute of its own.
func (String) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON hides the value from every JSON encoder. A handler that writes a whole structure
// reflects into its fields, which reaches the secret without asking LogValue.
func (String) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText hides the value from an encoder that asks for text, such as YAML.
func (String) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Reveal gives the value to the code that needs it.
func (s String) Reveal() string { return string(s) }

// Empty reports a secret that carries no value.
func (s String) Empty() bool { return s == "" }
