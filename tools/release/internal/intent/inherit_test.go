package intent_test

// Verdict-inheritance suite: drives intent.ClassifyReleaseOnly over the
// release-only corpus and intent.JudgeBaseVerdict over recorded GitHub facts.

import (
	_ "embed"
	"fmt"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/release_only_corpus.yaml
var releaseOnlyCorpusYAML []byte

//go:embed testdata/base_verdict_corpus.yaml
var baseVerdictCorpusYAML []byte

type releaseOnlyInput struct {
	Config  intent.Config       `yaml:"config"`
	Changes []intent.TreeChange `yaml:"changes"`
}

func TestClassifyReleaseOnly(t *testing.T) {
	corpus := loadNamedCorpus[releaseOnlyInput, intent.ReleaseOnlyResult](t, releaseOnlyCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := intent.ClassifyReleaseOnly(c.Input.Config, c.Input.Changes)
			if got != c.Expected {
				t.Fatalf("ClassifyReleaseOnly = %+v, want %+v", got, c.Expected)
			}
			if got.ReleaseOnly != (c.Classification == testcase.MustPass) {
				t.Fatalf("release_only = %t contradicts classification %s", got.ReleaseOnly, c.Classification)
			}
		})
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
