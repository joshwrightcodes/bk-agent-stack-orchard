//go:build contract

// This test reads the Orchard source at the pinned version, so it needs the network. The build
// tag keeps it out of `go test ./...`, and `mise run check-orchard-contract` runs it.
//
// It exists because nothing generates this client. The licences of the two projects do not
// combine, and the published description of the API omits fields that the controller uses, so
// the Go tags of the Orchard types are the contract. This test compares those tags with the tags
// of the structures in this package and in the specification builder, by reflection, so the
// comparison follows the code and needs no list that a person keeps current.
package orchard_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/orchard"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/vmspec"
)

// contract pairs a local structure with the upstream type and file that must carry its fields.
type contract struct {
	name     string // what this project calls it, for a message
	value    any    // the structure whose json tags this project uses
	file     string // the upstream file that declares the type
	upstream string // the upstream type
}

func contracts() []contract {
	return []contract{
		{"vmspec.VM", vmspec.VM{}, "pkg/resource/v1/v1.go", "VM"},
		{"vmspec.Script", vmspec.Script{}, "pkg/resource/v1/v1.go", "VMScript"},
		{"orchard.VM", orchard.VM{}, "pkg/resource/v1/v1.go", "VM"},
		{"orchard.Event", orchard.Event{}, "pkg/resource/v1/v1.go", "Event"},
		{"orchard.Worker", orchard.Worker{}, "pkg/resource/v1/worker.go", "Worker"},
	}
}

func TestTheOrchardContractStillHoldsAtThePinnedVersion(t *testing.T) {
	// ORCHARD_VERSION checks a candidate release before the documents pin it.
	version := os.Getenv("ORCHARD_VERSION")
	if version == "" {
		version = pinnedVersion(t)
	}
	t.Logf("checking the contract against Orchard %s", version)
	tags, embeds := upstreamTags(t, version)

	for _, c := range contracts() {
		t.Run(c.name, func(t *testing.T) {
			upstream := resolve(c.upstream, tags, embeds)
			if len(upstream) == 0 {
				t.Fatalf("Orchard %s has no type %s, which %s follows", version, c.upstream, c.name)
			}

			for _, field := range jsonTags(c.value) {
				if !upstream[field] {
					t.Errorf("%s uses the field %q, and Orchard %s %s no longer has it",
						c.name, field, version, c.upstream)
				}
			}
		})
	}
}

// jsonTags gives the wire names of a structure, which is what this project depends on.
func jsonTags(value any) []string {
	var names []string

	t := reflect.TypeOf(value)
	for i := range t.NumField() {
		tag, ok := t.Field(i).Tag.Lookup("json")
		if !ok {
			continue
		}

		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}

	return names
}

// pinnedVersion reads the minimum Orchard version out of the design, so that this test follows
// the document instead of a second copy of the version.
func pinnedVersion(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile("../../docs/architecture/target-design.md")
	if err != nil {
		t.Fatalf("reading the design: %v", err)
	}

	match := regexp.MustCompile(`minimum Orchard version MUST be ([0-9.]+)`).FindSubmatch(body)
	if match == nil {
		t.Fatal("the design no longer states a minimum Orchard version")
	}

	return string(match[1])
}

var (
	tagPattern      = regexp.MustCompile("`json:\"([^\",]+)")
	typePattern     = regexp.MustCompile(`^type (\w+) struct`)
	embeddedPattern = regexp.MustCompile(`^\t([A-Z]\w*)$`)
)

// upstreamTags reads the Orchard source at a version and gives the json tags of every structure,
// with the structures that each one embeds. Nothing is written to this repository.
func upstreamTags(t *testing.T, version string) (map[string]map[string]bool, map[string][]string) {
	t.Helper()

	tags := map[string]map[string]bool{}
	embeds := map[string][]string{}

	for _, file := range []string{"pkg/resource/v1/v1.go", "pkg/resource/v1/worker.go"} {
		source := fetch(t, file, version)

		var current string
		for _, line := range strings.Split(source, "\n") {
			switch {
			case typePattern.MatchString(line):
				current = typePattern.FindStringSubmatch(line)[1]
				tags[current] = map[string]bool{}
			case current == "":
			case tagPattern.MatchString(line):
				tags[current][tagPattern.FindStringSubmatch(line)[1]] = true
			case embeddedPattern.MatchString(line):
				embeds[current] = append(embeds[current], embeddedPattern.FindStringSubmatch(line)[1])
			}
		}
	}

	return tags, embeds
}

func fetch(t *testing.T, file, version string) string {
	t.Helper()

	url := "https://api.github.com/repos/openai/orchard/contents/" + file + "?ref=" + version

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	request.Header.Set("User-Agent", "bk-agent-stack-orchard")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("reading %s at %s: %v", file, version, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("reading %s at %s: %s", file, version, response.Status)
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("reading the answer for %s: %v", file, err)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body.Content, "\n", ""))
	if err != nil {
		t.Fatalf("decoding %s: %v", file, err)
	}

	return string(decoded)
}

// resolve gives every tag of a structure, including the tags of what it embeds, because a
// structure that embeds another carries its fields on the wire.
func resolve(name string, tags map[string]map[string]bool, embeds map[string][]string) map[string]bool {
	out := map[string]bool{}

	var walk func(string, map[string]bool)
	walk = func(current string, seen map[string]bool) {
		if seen[current] {
			return
		}
		seen[current] = true

		for field := range tags[current] {
			out[field] = true
		}
		for _, embedded := range embeds[current] {
			walk(embedded, seen)
		}
	}

	walk(name, map[string]bool{})

	return out
}
