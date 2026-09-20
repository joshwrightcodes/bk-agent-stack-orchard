package match_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/match"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

func profiles() []profile.Profile {
	return []profile.Profile{
		{Name: "default", Match: map[string]string{}},
		{Name: "xcode-27", Match: map[string]string{"xcode": "27"}},
		{Name: "xcode-27-large", Match: map[string]string{"xcode": "27", "size": "large"}},
	}
}

func TestMatchSelectsTheProfileWhoseTagsAreEqual(t *testing.T) {
	cases := map[string]struct {
		rules []string
		want  string
	}{
		"one tag":            {[]string{"queue=macos", "xcode=27"}, "xcode-27"},
		"two tags":           {[]string{"queue=macos", "xcode=27", "size=large"}, "xcode-27-large"},
		"another order":      {[]string{"size=large", "xcode=27", "queue=macos"}, "xcode-27-large"},
		"the queue only":     {[]string{"queue=macos"}, "default"},
		"no rule at all":     {nil, "default"},
		"a value with signs": {[]string{"queue=macos", "xcode=27"}, "xcode-27"},
	}

	for name, c := range cases {
		got, err := match.Match(c.rules, profiles())
		if err != nil {
			t.Errorf("Match(%s) = %v, want the profile %q", name, err, c.want)

			continue
		}
		if got.Name != c.want {
			t.Errorf("Match(%s) = %q, want %q", name, got.Name, c.want)
		}
	}
}

// A job with an extra tag must not run on a profile that ignores the tag, because the pipeline
// asked for something that the profile does not give.
func TestMatchRefusesAProfileThatIgnoresAnExtraTag(t *testing.T) {
	_, err := match.Match([]string{"queue=macos", "xcode=27", "size=large", "gpu=yes"}, profiles())

	var mismatch *match.Mismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("Match() = %v, want a mismatch", err)
	}
	if mismatch.Reason != match.ReasonNoProfile {
		t.Errorf("Reason = %q, want %q", mismatch.Reason, match.ReasonNoProfile)
	}
}

func TestMatchRefusesAJobThatMissesATag(t *testing.T) {
	if _, err := match.Match([]string{"queue=macos", "size=large"}, profiles()); err == nil {
		t.Error("Match() with a tag that no profile has = nil, want a mismatch")
	}
}

// The queue selects the fleet, not the profile, so the rule that carries it takes no part in the
// comparison.
func TestMatchIgnoresTheQueueRule(t *testing.T) {
	with, err := match.Match([]string{"queue=macos", "xcode=27"}, profiles())
	if err != nil {
		t.Fatalf("Match() = %v", err)
	}

	without, err := match.Match([]string{"xcode=27"}, profiles())
	if err != nil {
		t.Fatalf("Match() without the queue rule = %v", err)
	}

	if with.Name != without.Name {
		t.Errorf("the queue rule changed the profile: %q and %q", with.Name, without.Name)
	}
}

// A rule that is not a literal cannot select exactly one profile. The job fails with a message
// instead of running somewhere that the pipeline did not ask for.
func TestMatchRefusesARuleThatIsNotALiteral(t *testing.T) {
	cases := map[string][]string{
		"a wildcard value": {"queue=macos", "xcode=27*"},
		"a wildcard tag":   {"queue=macos", "*=27"},
		"a negation":       {"queue=macos", "xcode!=26"},
		"no equals sign":   {"queue=macos", "xcode"},
		"an empty key":     {"queue=macos", "=27"},
		"an empty value":   {"queue=macos", "xcode="},
	}

	for name, rules := range cases {
		_, err := match.Match(rules, profiles())

		var mismatch *match.Mismatch
		if !errors.As(err, &mismatch) {
			t.Errorf("Match(%s) = %v, want a mismatch", name, err)

			continue
		}
		if mismatch.Reason != match.ReasonRuleNotLiteral {
			t.Errorf("Match(%s) gave the reason %q, want %q",
				name, mismatch.Reason, match.ReasonRuleNotLiteral)
		}
	}
}

func TestMatchRefusesTheSameTagTwiceWithTwoValues(t *testing.T) {
	if _, err := match.Match([]string{"queue=macos", "xcode=27", "xcode=26"}, profiles()); err == nil {
		t.Error("Match() with two values for one tag = nil, want a mismatch")
	}
}

func TestMatchAcceptsTheSameTagTwiceWithOneValue(t *testing.T) {
	got, err := match.Match([]string{"queue=macos", "xcode=27", "xcode=27"}, profiles())
	if err != nil {
		t.Fatalf("Match() = %v, want the profile", err)
	}
	if got.Name != "xcode-27" {
		t.Errorf("Match() = %q, want xcode-27", got.Name)
	}
}

// The message goes to the job page of a pipeline author, who needs to see what the job asked for
// and what the fleet offers.
func TestMismatchMessageListsTheTagsAndEveryProfile(t *testing.T) {
	_, err := match.Match([]string{"queue=macos", "xcode=99"}, profiles())
	if err == nil {
		t.Fatal("Match() = nil, want a mismatch")
	}

	message := err.Error()
	for _, want := range []string{"xcode=99", "default", "xcode-27", "xcode-27-large", "size=large"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not contain %q: %s", want, message)
		}
	}
	if strings.Contains(message, "queue=macos") {
		t.Errorf("the message contains the queue rule, which takes no part in the match: %s", message)
	}
}

func TestMatchWithNoProfileGivesAMismatch(t *testing.T) {
	if _, err := match.Match([]string{"queue=macos"}, nil); err == nil {
		t.Error("Match() with no profile = nil, want a mismatch")
	}
}

// Two profiles cannot share a match map, because the configuration refuses that at start-up.
// The matcher still reports it rather than selecting the first, so a defect cannot hide.
func TestMatchReportsTwoProfilesThatMatchTheSameJob(t *testing.T) {
	ambiguous := []profile.Profile{
		{Name: "first", Match: map[string]string{"xcode": "27"}},
		{Name: "second", Match: map[string]string{"xcode": "27"}},
	}

	_, err := match.Match([]string{"queue=macos", "xcode=27"}, ambiguous)

	var mismatch *match.Mismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("Match() = %v, want a mismatch", err)
	}
	if mismatch.Reason != match.ReasonAmbiguous {
		t.Errorf("Reason = %q, want %q", mismatch.Reason, match.ReasonAmbiguous)
	}
}

func TestTagsReadsTheRulesWithoutTheQueue(t *testing.T) {
	tags, err := match.Tags([]string{"queue=macos", "xcode=27"})
	if err != nil {
		t.Fatalf("Tags() = %v", err)
	}

	if len(tags) != 1 || tags["xcode"] != "27" {
		t.Errorf("Tags() = %v, want only the xcode tag", tags)
	}
}
