package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// DefaultPath is the conventional location of a repo's merge-queue policy
// file, relative to the repository root. It is the one owner of that path:
// the CLI default and every message that names the file read it from here.
const DefaultPath = ".github/release-queue.policy.yml"

// QueuePolicy is a repo's merge-queue policy: the required contexts, the
// release job, and the bot jobs the checker holds the workflows directory to.
// It is the whole content of the policy file, whose only top-level key is
// 'queue:'.
type QueuePolicy struct {
	Queue QueueSection
}

// queuePolicyFile is the on-disk shape. The pointer tells a file with no
// 'queue:' key apart from one whose section is empty, so each gets its own
// message.
type queuePolicyFile struct {
	Queue *QueueSection `yaml:"queue"`
}

// LoadQueuePolicy reads and parses a repo's merge-queue policy file into a
// QueuePolicy. Unknown fields are rejected at every level, so a typo'd key
// fails loudly instead of silently disabling a rule, and a file without a
// 'queue:' section is an error: this file exists only to carry it.
func LoadQueuePolicy(path string) (QueuePolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return QueuePolicy{}, fmt.Errorf("load queue policy: cannot read %s during merge-queue policy validation: %w. Create the merge-queue policy file (%s) or fix the %s path", path, err, DefaultPath, intent.FlagPolicy)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var file queuePolicyFile
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return QueuePolicy{}, fmt.Errorf("load queue policy: cannot parse %s as a merge-queue policy: %w. Expected one top-level 'queue:' mapping of {required, release_job, bot_jobs} and no other key", path, err)
	}
	if file.Queue == nil {
		return QueuePolicy{}, fmt.Errorf("load queue policy: %s has no 'queue:' section during merge-queue policy validation, so no rule could be checked. Add a top-level 'queue:' mapping that lists at least the title-guard context under 'required:'", path)
	}
	if err := validateQueueSection(path, *file.Queue); err != nil {
		return QueuePolicy{}, err
	}
	return QueuePolicy{Queue: *file.Queue}, nil
}
