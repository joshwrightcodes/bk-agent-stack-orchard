// Package match selects the virtual machine profile of a job from the agent tags of that job.
//
// The match is exact: the tags of the job, without the queue, must equal the match map of one
// profile. A subset match would let a job with an extra tag run on a profile that ignores the
// tag, which gives the pipeline something that it did not ask for.
package match

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
)

// queueKey selects the fleet, not the profile, so it takes no part in the comparison.
const queueKey = "queue"

// Reasons that a job has no profile.
const (
	// ReasonRuleNotLiteral is a rule that is not `key=literal`: a wildcard, a negation, or a
	// rule with another shape.
	ReasonRuleNotLiteral = "a rule is not a literal tag"

	// ReasonNoProfile is a job whose tags equal the match map of no profile.
	ReasonNoProfile = "no profile has these tags"

	// ReasonAmbiguous is two profiles with the same match map. The configuration refuses this at
	// start-up, so a job that meets it shows a defect rather than a mistake of the pipeline.
	ReasonAmbiguous = "more than one profile has these tags"
)

// Mismatch says why a job has no profile. Its message goes to the job page, so it names the tags
// of the job and the tags of every profile.
type Mismatch struct {
	Reason   string
	Tags     map[string]string
	Rule     string
	Profiles []profile.Profile
}

// Error gives the message that a pipeline author reads on the job page.
func (m *Mismatch) Error() string {
	var b strings.Builder

	b.WriteString("this job has no profile: ")
	b.WriteString(m.Reason)

	if m.Rule != "" {
		fmt.Fprintf(&b, " (%q)", m.Rule)
	}

	fmt.Fprintf(&b, ". The job asks for {%s}. ", formatTags(m.Tags))

	if len(m.Profiles) == 0 {
		b.WriteString("The fleet has no profile.")

		return b.String()
	}

	b.WriteString("The fleet offers: ")

	offers := make([]string, 0, len(m.Profiles))
	for _, p := range m.Profiles {
		offers = append(offers, fmt.Sprintf("%s {%s}", p.Name, formatTags(p.Match)))
	}
	b.WriteString(strings.Join(offers, ", "))
	b.WriteString(".")

	return b.String()
}

// formatTags writes a tag map in a stable order, so that two messages of the same job read the
// same way.
func formatTags(tags map[string]string) string {
	if len(tags) == 0 {
		return "no tags"
	}

	pairs := make([]string, 0, len(tags))
	for key, value := range tags {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)

	return strings.Join(pairs, ", ")
}

// Tags reads the agent query rules of a job into a map, without the queue rule.
//
// A rule must be `key=literal`. A wildcard, a negation, or another shape gives a mismatch,
// because such a rule can select more than one profile or none.
func Tags(rules []string) (map[string]string, error) {
	tags := make(map[string]string, len(rules))

	for _, rule := range rules {
		key, value, found := strings.Cut(rule, "=")
		switch {
		case !found, key == "", value == "":
			return nil, &Mismatch{Reason: ReasonRuleNotLiteral, Rule: rule}
		case strings.ContainsAny(key, "*?!"), strings.ContainsAny(value, "*?"):
			return nil, &Mismatch{Reason: ReasonRuleNotLiteral, Rule: rule}
		}

		if previous, seen := tags[key]; seen && previous != value {
			return nil, &Mismatch{
				Reason: ReasonRuleNotLiteral,
				Rule:   fmt.Sprintf("%s=%s and %s=%s", key, previous, key, value),
			}
		}

		tags[key] = value
	}

	delete(tags, queueKey)

	return tags, nil
}

// Match selects the one profile whose match map equals the tags of the job.
//
// The error is a *Mismatch, and its message is what the controller writes on the job page when
// it finishes the job.
func Match(rules []string, profiles []profile.Profile) (profile.Profile, error) {
	tags, err := Tags(rules)
	if err != nil {
		// The mismatch of Tags knows the rule that failed. It does not know the job or the
		// fleet, and the message on the job page needs both.
		var mismatch *Mismatch
		if errors.As(err, &mismatch) {
			mismatch.Tags = tagsOrEmpty(rules)
			mismatch.Profiles = profiles
		}

		return profile.Profile{}, err
	}

	var found []profile.Profile
	for _, p := range profiles {
		if maps.Equal(p.Match, tags) {
			found = append(found, p)
		}
	}

	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return profile.Profile{}, &Mismatch{Reason: ReasonNoProfile, Tags: tags, Profiles: profiles}
	default:
		return profile.Profile{}, &Mismatch{Reason: ReasonAmbiguous, Tags: tags, Profiles: profiles}
	}
}

// tagsOrEmpty gives what the rules say, so that a message can show the tags of a job whose rules
// are not all literal.
func tagsOrEmpty(rules []string) map[string]string {
	tags := make(map[string]string, len(rules))

	for _, rule := range rules {
		key, value, found := strings.Cut(rule, "=")
		if found && key != queueKey {
			tags[key] = value
		}
	}

	return tags
}
