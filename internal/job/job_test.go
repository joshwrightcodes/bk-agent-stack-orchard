package job_test

import (
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/job"
)

func TestParseIDAcceptsWhatBuildkiteGives(t *testing.T) {
	for _, text := range []string{
		"018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d", // version 7, which Buildkite uses
		"01234567-89ab-4cde-8f01-23456789abcd", // version 4
	} {
		id, err := job.ParseID(text)
		if err != nil {
			t.Errorf("ParseID(%q) = %v, want no error", text, err)
		}
		if id.String() != text {
			t.Errorf("ParseID(%q).String() = %q", text, id)
		}
	}
}

func TestParseIDRejectsTextThatIsNotAJob(t *testing.T) {
	for name, text := range map[string]string{
		"empty":             "",
		"a virtual machine": "bk-macos-prod-018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d",
		"a profile":         "xcode-27",
		"upper case":        "018F3B2C-1C4A-7E5B-9A3D-2F1E4C5B6A7D",
		"too short":         "018f3b2c-1c4a-7e5b-9a3d",
	} {
		if _, err := job.ParseID(text); err == nil {
			t.Errorf("ParseID(%s) = nil, want an error", name)
		}
	}
}

func TestValidReportsAnIdentifierThatSomebodyBuiltWithoutParsing(t *testing.T) {
	if job.ID("not-a-job").Valid() {
		t.Error("Valid() = true for text that is not an identifier")
	}
	if !job.ID("018f3b2c-1c4a-7e5b-9a3d-2f1e4c5b6a7d").Valid() {
		t.Error("Valid() = false for an identifier")
	}
}
