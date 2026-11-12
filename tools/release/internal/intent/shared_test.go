package intent_test

// Shared-vocabulary suite: drives the git ref parser and builders over the
// git ref corpus and the output-line parser over the output corpus.

import (
	_ "embed"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/gitref_corpus.yaml
var gitRefCorpusYAML []byte

//go:embed testdata/output_corpus.yaml
var outputCorpusYAML []byte

type gitRefInput struct {
	Raw string `yaml:"raw"`
}

type gitRefExpected struct {
	Branch string                `yaml:"branch"`
	Tag    string                `yaml:"tag"`
	Group  *intent.MergeQueueRef `yaml:"group"`
}

func TestGitRef(t *testing.T) {
	corpus := loadNamedCorpus[gitRefInput, gitRefExpected](t, gitRefCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			ref, err := intent.ParseGitRef(c.Input.Raw)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("ParseGitRef(%q): expected an error, got %q", c.Input.Raw, ref)
				}
				requireSixLabels(t, err)
				return
			}
			if err != nil {
				t.Fatalf("ParseGitRef(%q): unexpected error: %v", c.Input.Raw, err)
			}
			if ref.String() != c.Input.Raw {
				t.Errorf("ParseGitRef(%q) = %q, want the same text", c.Input.Raw, ref)
			}

			branch, isBranch := ref.Branch()
			if branch != c.Expected.Branch || isBranch != (c.Expected.Branch != "") {
				t.Errorf("Branch() = %q, %v, want %q", branch, isBranch, c.Expected.Branch)
			}
			if isBranch && intent.BranchRef(branch) != ref {
				t.Errorf("BranchRef(%q) = %q, want %q", branch, intent.BranchRef(branch), ref)
			}

			tag, isTag := ref.Tag()
			if tag != c.Expected.Tag || isTag != (c.Expected.Tag != "") {
				t.Errorf("Tag() = %q, %v, want %q", tag, isTag, c.Expected.Tag)
			}
			if isTag && intent.TagRef(tag) != ref {
				t.Errorf("TagRef(%q) = %q, want %q", tag, intent.TagRef(tag), ref)
			}

			group, isGroup := intent.ParseMergeQueueRef(ref)
			if c.Expected.Group == nil {
				if isGroup {
					t.Errorf("ParseMergeQueueRef(%q) = %+v, want no merge group", ref, group)
				}
				return
			}
			if !isGroup {
				t.Fatalf("ParseMergeQueueRef(%q): want %+v, got no merge group", ref, *c.Expected.Group)
			}
			if group != *c.Expected.Group {
				t.Errorf("ParseMergeQueueRef(%q) = %+v, want %+v", ref, group, *c.Expected.Group)
			}
			if built := group.Ref(); built != ref {
				t.Errorf("MergeQueueRef.Ref() = %q, want %q", built, ref)
			}
		})
	}
}

type outputInput struct {
	Line string `yaml:"line"`
}

type outputExpected struct {
	Key   intent.OutputKey `yaml:"key"`
	Value string           `yaml:"value"`
}

func TestOutputLine(t *testing.T) {
	corpus := loadNamedCorpus[outputInput, outputExpected](t, outputCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			key, value, err := intent.ParseOutputLine(c.Input.Line)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("ParseOutputLine(%q): expected an error, got %q=%q", c.Input.Line, key, value)
				}
				requireSixLabels(t, err)
				return
			}
			if err != nil {
				t.Fatalf("ParseOutputLine(%q): unexpected error: %v", c.Input.Line, err)
			}
			if key != c.Expected.Key || value != c.Expected.Value {
				t.Errorf("ParseOutputLine(%q) = %q, %q, want %q, %q", c.Input.Line, key, value, c.Expected.Key, c.Expected.Value)
			}
			if line := key.Line(value); line != c.Input.Line {
				t.Errorf("OutputKey.Line = %q, want %q", line, c.Input.Line)
			}
		})
	}
}
