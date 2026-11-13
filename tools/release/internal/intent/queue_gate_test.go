package intent_test

// Queue-gate suite: drives intent.QueueGate over the gate corpus.

import (
	_ "embed"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/queue_gate_corpus.yaml
var queueGateCorpusYAML []byte

type queueGateInput struct {
	Current   string              `yaml:"current"`
	ReleasePR int                 `yaml:"release_pr"`
	Entries   []intent.QueueEntry `yaml:"entries"`
}

type queueGateExpected struct {
	Gate intent.Gate `yaml:"gate"`
}

func TestQueueGate(t *testing.T) {
	corpus := loadNamedCorpus[queueGateInput, queueGateExpected](t, queueGateCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			cfg := intent.Config{Releases: true}
			got, err := intent.QueueGate(cfg, intent.SemVer(c.Input.Current), c.Input.Entries, c.Input.ReleasePR)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("QueueGate: expected an error, got %q", got)
				}
				requireSixLabels(t, err)
				return
			}
			if err != nil {
				t.Fatalf("QueueGate: unexpected error: %v", err)
			}
			if got != c.Expected.Gate {
				t.Errorf("QueueGate = %q, want %q", got, c.Expected.Gate)
			}
		})
	}
}
