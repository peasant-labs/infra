package intent_test

// Version-field suite: drives intent.ReadVersionField and
// intent.WriteVersionField over the version-field corpus. Writes must be
// byte-exact outside the value; failures must be plain six-part errors.

import (
	_ "embed"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/versionfield_corpus.yaml
var versionFieldCorpusYAML []byte

// versionFieldOp is the closed set of operations a version-field row drives.
type versionFieldOp string

const (
	opRead  versionFieldOp = "read"
	opWrite versionFieldOp = "write"
)

var allVersionFieldOps = []versionFieldOp{opRead, opWrite}

func (o versionFieldOp) String() string { return string(o) }

// UnmarshalText refuses any operation outside the closed set at load time.
func (o *versionFieldOp) UnmarshalText(text []byte) error {
	op, err := closedset.Parse(allVersionFieldOps, string(text), closedset.Context{
		Set:   "version-field op",
		Where: "testdata/versionfield_corpus.yaml",
		When:  "loading the version-field corpus",
		Means: "the row drives no operation, so the corpus does not load",
	})
	if err != nil {
		return err
	}
	*o = op
	return nil
}

type versionFieldInput struct {
	Op       versionFieldOp `yaml:"op"`
	Document string         `yaml:"document"`
	Field    string         `yaml:"field"`
	Value    string         `yaml:"value"`
}

type versionFieldExpected struct {
	Output string `yaml:"output"`
}

func TestVersionField(t *testing.T) {
	corpus := loadNamedCorpus[versionFieldInput, versionFieldExpected](t, versionFieldCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Input.Op {
			case opRead:
				got, err := intent.ReadVersionField([]byte(c.Input.Document), c.Input.Field)
				if c.Classification == testcase.MustFail {
					if err == nil {
						t.Fatalf("ReadVersionField: expected an error, got %q", got)
					}
					requireSixLabels(t, err)
					return
				}
				if err != nil {
					t.Fatalf("ReadVersionField: unexpected error: %v", err)
				}
				if got != c.Expected.Output {
					t.Errorf("ReadVersionField = %q, want %q", got, c.Expected.Output)
				}
			case opWrite:
				got, err := intent.WriteVersionField([]byte(c.Input.Document), c.Input.Field, c.Input.Value)
				if c.Classification == testcase.MustFail {
					if err == nil {
						t.Fatalf("WriteVersionField: expected an error, got %q", got)
					}
					requireSixLabels(t, err)
					return
				}
				if err != nil {
					t.Fatalf("WriteVersionField: unexpected error: %v", err)
				}
				if string(got) != c.Expected.Output {
					t.Errorf("WriteVersionField = %q, want %q", got, c.Expected.Output)
				}
			default:
				t.Fatalf("unknown op %q: want one of %v", c.Input.Op, allVersionFieldOps)
			}
		})
	}
}
