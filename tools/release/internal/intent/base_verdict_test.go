package intent_test

// Base-verdict suite: drives intent.JudgeBaseVerdict over recorded GitHub
// facts and pins the versioned marker step set.

import (
	"bytes"
	_ "embed"
	"fmt"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/base_verdict_corpus.yaml
var baseVerdictCorpusYAML []byte

//go:embed testdata/verdict_markers.yaml
var verdictMarkersYAML []byte

// TestVerdictMarkerStepNames pins the exported marker set and its version:
// a change to either must change the fixture consumers mirror.
func TestVerdictMarkerStepNames(t *testing.T) {
	var pinned struct {
		Version int      `yaml:"version"`
		Names   []string `yaml:"names"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(verdictMarkersYAML))
	decoder.KnownFields(true)
	if err := decoder.Decode(&pinned); err != nil {
		t.Fatalf("decode testdata/verdict_markers.yaml: %v", err)
	}
	if pinned.Version != intent.VerdictMarkersVersion {
		t.Fatalf("VerdictMarkersVersion = %d, fixture pins %d", intent.VerdictMarkersVersion, pinned.Version)
	}
	if got := intent.VerdictMarkerStepNames(); !slices.Equal(got, pinned.Names) {
		t.Fatalf("VerdictMarkerStepNames() = %q, fixture pins %q: bump VerdictMarkersVersion and the fixture together", got, pinned.Names)
	}
}

// baseVerdictRule is schema's rule: the facts in the corpus are its runs.
var baseVerdictRule = intent.InheritRule{Context: "merge-gate", WorkflowPath: ".github/workflows/tests.yml", FullSuiteName: "make check"}

const baseVerdictSHA = "adf33e168bd7f57f5a3d0ef9786366c554ec1662"

type baseVerdictInput struct {
	CheckRuns []intent.CheckRunFact `yaml:"check_runs"`
	Jobs      []intent.JobFact      `yaml:"jobs"`
	Runs      []intent.RunFact      `yaml:"runs"`
	RunJobs   []intent.JobFact      `yaml:"run_jobs"`
}

type baseVerdictExpected struct {
	Source intent.VerdictSource `yaml:"source"`
	Code   intent.ErrorCode     `yaml:"code"`
}

// factReader serves recorded facts. A read the facts do not hold is an
// error, so a judgment that reads more than its row records fails loudly.
type factReader struct{ in baseVerdictInput }

func (r factReader) CheckRuns(sha string) ([]intent.CheckRunFact, error) {
	if sha != baseVerdictSHA {
		return nil, fmt.Errorf("unexpected commit %s", sha)
	}
	return r.in.CheckRuns, nil
}

func (r factReader) Job(id int64) (intent.JobFact, error) {
	for _, j := range r.in.Jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return intent.JobFact{}, fmt.Errorf("no recorded job %d", id)
}

func (r factReader) Run(id int64) (intent.RunFact, error) {
	for _, run := range r.in.Runs {
		if run.ID == id {
			return run, nil
		}
	}
	return intent.RunFact{}, fmt.Errorf("no recorded run %d", id)
}

func (r factReader) RunJobs(id int64) ([]intent.JobFact, error) {
	var out []intent.JobFact
	for _, j := range r.in.RunJobs {
		if j.RunID == id {
			out = append(out, j)
		}
	}
	return out, nil
}

func TestJudgeBaseVerdict(t *testing.T) {
	corpus := loadNamedCorpus[baseVerdictInput, baseVerdictExpected](t, baseVerdictCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			source, err := intent.JudgeBaseVerdict(baseVerdictRule, baseVerdictSHA, factReader{c.Input})
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("JudgeBaseVerdict = %s, want rejection %s", source, c.Expected.Code)
				}
				requireSixParts(t, err, c.Expected.Code)
				want := c.Expected.Code == intent.ErrBaseVerdictPending || c.Expected.Code == intent.ErrBaseVerdictMissing
				if intent.IsPendingVerdict(err) != want {
					t.Fatalf("IsPendingVerdict(%s) = %t, want %t", c.Expected.Code, !want, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("JudgeBaseVerdict: unexpected error: %v", err)
			}
			if source != c.Expected.Source {
				t.Fatalf("source = %s, want %s", source, c.Expected.Source)
			}
		})
	}
}
