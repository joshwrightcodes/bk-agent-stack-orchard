// SPDX-License-Identifier: AGPL-3.0-or-later
// SPDX-FileCopyrightText: 2026 Josh Wright

package main

import (
	"fmt"
	"strings"
	"testing"
)

// packet writes one packet line of a reference list, with its four-digit hexadecimal length.
func packet(line string) string {
	return fmt.Sprintf("%04x%s", len(line)+4, line)
}

// advertisement builds the reference list that a git server sends before a clone, in the shape
// that github.com sends it: a service line, a flush, the references, and a final flush.
func advertisement(refs ...string) []byte {
	var b strings.Builder

	b.WriteString(packet("# service=git-upload-pack\n"))
	b.WriteString("0000")

	for i, ref := range refs {
		if i == 0 {
			// The first reference carries the capabilities of the server after a NUL byte.
			ref += "\x00multi_ack thin-pack symref=HEAD:refs/heads/main"
		}

		b.WriteString(packet(ref + "\n"))
	}

	b.WriteString("0000")

	return []byte(b.String())
}

const (
	commitA = "22bd63c34674a1422896aa208a464f4340cdb0b8"
	commitB = "24670c97fa5eeb9f9646cd9eb6faae63dc66a8d8"
	tagObj  = "70409ac446f8a837c78e8c2c0cc0e84f1eee80ad"
)

// A lightweight tag names its commit directly.
func TestTagCommitReadsALightweightTag(t *testing.T) {
	list := advertisement(
		commitB+" HEAD",
		commitB+" refs/heads/main",
		commitA+" refs/tags/0.58.0",
		commitB+" refs/tags/0.58.1",
	)

	got, err := tagCommit(list, "0.58.0")
	if err != nil {
		t.Fatalf("tagCommit: %v", err)
	}

	if got != commitA {
		t.Errorf("tagCommit = %s, want %s", got, commitA)
	}
}

// An annotated tag names a tag object. The commit is on the peeled line that follows it, and
// comparing the tag object with the pinned commit would always fail.
func TestTagCommitPrefersThePeeledCommitOfAnAnnotatedTag(t *testing.T) {
	list := advertisement(
		commitB+" HEAD",
		tagObj+" refs/tags/0.58.0",
		commitA+" refs/tags/0.58.0^{}",
	)

	got, err := tagCommit(list, "0.58.0")
	if err != nil {
		t.Fatalf("tagCommit: %v", err)
	}

	if got != commitA {
		t.Errorf("tagCommit = %s, want %s", got, commitA)
	}
}

// A tag whose name starts with the wanted name is a different tag.
func TestTagCommitMatchesTheWholeName(t *testing.T) {
	list := advertisement(
		commitB+" HEAD",
		commitB+" refs/tags/0.58.0-rc1",
	)

	if got, err := tagCommit(list, "0.58.0"); err == nil {
		t.Errorf("tagCommit = %s, want an error for a missing tag", got)
	}
}

// The first reference of the list carries the capabilities, so the tag can be on that line too.
func TestTagCommitReadsTheFirstReference(t *testing.T) {
	got, err := tagCommit(advertisement(commitA+" refs/tags/0.58.0"), "0.58.0")
	if err != nil {
		t.Fatalf("tagCommit: %v", err)
	}

	if got != commitA {
		t.Errorf("tagCommit = %s, want %s", got, commitA)
	}
}

// A list that is cut short, or that is not a reference list at all, such as an HTML error page,
// stops the generator with an error instead of a wrong answer.
func TestTagCommitRejectsABrokenList(t *testing.T) {
	whole := string(advertisement(commitA + " refs/tags/0.58.0"))

	for name, list := range map[string]string{
		"cut short":  whole[:len(whole)-20],
		"not a list": "<!DOCTYPE html><html></html>",
		"short tail": whole + "00",
		"bad length": "0002",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := tagCommit([]byte(list), "0.58.0"); err == nil {
				t.Errorf("tagCommit = %s, want an error", got)
			}
		})
	}
}
