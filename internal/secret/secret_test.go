package secret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// A structure that carries a secret is logged whole in practice, so every path that writes a
// value must hide it.
type block struct {
	Queue string
	Token secret.String
}

func TestAStringDoesNotPrintItsValue(t *testing.T) {
	b := block{Queue: "macos", Token: secret.String("bkct_real_value")}

	printed := map[string]string{
		"%v of the structure":  fmt.Sprintf("%v", b),
		"%+v of the structure": fmt.Sprintf("%+v", b),
		"%#v of the structure": fmt.Sprintf("%#v", b),
		"%v of the secret":     fmt.Sprintf("%v", b.Token),
		"%q of the secret":     fmt.Sprintf("%q", b.Token),
		"%#v of the secret":    fmt.Sprintf("%#v", b.Token),
		"String of the secret": b.Token.String(),
	}

	for name, line := range printed {
		if strings.Contains(line, "bkct_real_value") {
			t.Errorf("%s printed the secret: %s", name, line)
		}
		if !strings.Contains(line, "redacted") {
			t.Errorf("%s does not say that a value is hidden: %s", name, line)
		}
	}
}

// A JSON encoder reflects into a structure, and a slog handler that writes a structure does the
// same, so neither asks LogValue about a field.
func TestAStringDoesNotEncodeItsValue(t *testing.T) {
	b := block{Queue: "macos", Token: secret.String("bkct_real_value")}

	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}
	if strings.Contains(string(encoded), "bkct_real_value") {
		t.Errorf("json.Marshal wrote the secret: %s", encoded)
	}

	var buffer bytes.Buffer
	slog.New(slog.NewJSONHandler(&buffer, nil)).Info("configuration", "block", b, "token", b.Token)

	if strings.Contains(buffer.String(), "bkct_real_value") {
		t.Errorf("slog wrote the secret: %s", buffer.String())
	}
}

func TestRevealGivesTheValue(t *testing.T) {
	if got := secret.String("bkct_real_value").Reveal(); got != "bkct_real_value" {
		t.Errorf("Reveal() = %q", got)
	}
}

func TestEmptyReportsASecretWithNoValue(t *testing.T) {
	if !secret.String("").Empty() {
		t.Error("Empty() = false for a secret with no value")
	}
	if secret.String("a").Empty() {
		t.Error("Empty() = true for a secret with a value")
	}
}
