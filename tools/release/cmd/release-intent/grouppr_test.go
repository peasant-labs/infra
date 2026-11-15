package main

import (
	"strconv"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

type groupPRInput struct {
	// Ref is the ref text under test, in its wire spelling.
	Ref string `yaml:"ref"`
}

type groupPRExpected struct {
	// PR is the number the ref names; 0 means the command prints an empty
	// value.
	PR int `yaml:"pr"`
}

// TestGroupPRNumber drives groupPRNumber over the ref corpus. For every ref
// that names a PR, the contract's own builder must render the same ref from
// its parsed parts, so the corpus and the typed owner cannot disagree.
func TestGroupPRNumber(t *testing.T) {
	t.Parallel()
	for _, c := range loadCorpus[groupPRInput, groupPRExpected](t, "group-pr.yaml") {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			want := ""
			if c.Expected.PR != 0 {
				want = strconv.Itoa(c.Expected.PR)
			}
			if got := groupPRNumber(c.Input.Ref); got != want {
				t.Fatalf("groupPRNumber(%q) = %q, want %q", c.Input.Ref, got, want)
			}
			if c.Expected.PR == 0 {
				return
			}
			if c.Classification != testcase.MustPass {
				t.Fatalf("%s: a row that names a PR is must-pass", c.Name)
			}
			ref, err := intent.ParseGitRef(c.Input.Ref)
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			group, ok := intent.ParseMergeQueueRef(ref)
			if !ok || group.PR != c.Expected.PR || group.Ref() != ref {
				t.Fatalf("%s: ParseMergeQueueRef(%q) = %+v, %v; want PR %d and a ref that renders back", c.Name, ref, group, ok, c.Expected.PR)
			}
		})
	}
}
