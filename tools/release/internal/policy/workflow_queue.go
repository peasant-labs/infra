package policy

import (
	"fmt"
	"slices"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// QueueRule names one merge-queue workflow rule the shared checker applies to
// every repo. The set is closed: adding a rule is a contract change, so callers
// compare against these constants rather than bare strings and diagnostics
// carry the member that fired.
//
// Each member names the workflow shape it pins: which workflows trigger on
// the merge queue and report unfiltered, how the release job gates every
// write on the planner's action, how the title guard stays honest, how range
// readers take the floor, how every credential stays in the listed jobs and
// runs only closed templates there, and how the tool pin, the cut button,
// and the title checks stay exact.
// Required-contexts entries must resolve, and the operator compares the
// printed list with the ruleset. The token rule and the classify rule keep
// one classifier, no branch literals, and the ref checked before the App key
// exists, and the tool-call rule holds every invocation to the CLI's flags.
type QueueRule string

const (
	// QueueRuleRequiredContexts pins every required check the merge queue waits
	// on: each entry names a workflow file in the workflows directory and the
	// job inside it that reports the context. The checker returns the list for
	// the operator to compare against the queue ruleset.
	QueueRuleRequiredContexts QueueRule = "required-contexts"
	// QueueRuleMergeGroup requires every workflow that owns a required context
	// to trigger on merge_group as well as on pull_request.
	QueueRuleMergeGroup QueueRule = "merge-group-on-required-workflows"
	// QueueRuleConcurrency requires every workflow that owns a required context
	// to key its concurrency group on the ref or the run id, and to cancel
	// in-progress runs only for pull_request. A group keyed only on the PR
	// number is empty on a merge group, so every group build would share one
	// group and cancel all but one pending run.
	QueueRuleConcurrency QueueRule = "required-workflows-concurrency-per-ref"
	// QueueRuleReleaseActions gates every step of the release job that can
	// write on the action the tool printed in the same run, in the canonical
	// verdict form. A tool output is not a gate by itself: a needs edge is
	// satisfied by any job that succeeds and an exit 0 in one step does not
	// stop the next step.
	QueueRuleReleaseActions QueueRule = "release-actions-gated"
	// QueueRuleTitleGuard keeps the title guard honest: the guard job carries
	// no job-level if and no continue-on-error, and the guard workflow never
	// reads the pull-request action or the changed-file list. A run that ends
	// green without checking would replace a red run, and a body edit does not
	// change the head commit.
	QueueRuleTitleGuard QueueRule = "title-guard-no-early-success"
	// QueueRuleRangeFloor requires every workflow step that reads a commit
	// range to pass the recovery floor from its repository variable, so a
	// subject the tool cannot parse leaves parsing everywhere at once.
	QueueRuleRangeFloor QueueRule = "range-readers-take-the-floor"
	// QueueRuleTokenReadOnly holds the default workflow token to reads:
	// every workflow and every job declares permissions; every scope is read
	// or none, except id-token write (OIDC publishing, no ref) and contents
	// write in the workflow that runs only on a pushed release tag; and
	// every actions/checkout sets persist-credentials: false.
	QueueRuleTokenReadOnly QueueRule = "default-token-read-only"
	// QueueRuleCredentialScope keeps every credential (the minted App token,
	// the secrets that mint it, a default token that can write) inside the
	// release job, the listed bot jobs and the publish jobs, and there only
	// in the mint step's inputs and the env of a run step: never a workflow
	// or job env, a job output, a reusable workflow, an action's inputs, or
	// a run block's text.
	QueueRuleCredentialScope QueueRule = "credentials-stay-in-listed-jobs"
	// QueueRuleCredentialActions holds every job that may hold a credential
	// to the allowed actions, each pinned to a 40-hex commit, with no
	// reusable workflow and no container.
	QueueRuleCredentialActions QueueRule = "credential-jobs-use-pinned-actions"
	// QueueRuleCredentialTemplates holds every step that receives a
	// credential to exactly one command from the closed template set its
	// job may run, built from the release config and the job's own bot kind.
	// It also resolves each listed job's bot branch: a kind the config does
	// not name, or a kind two jobs claim, is reported here.
	QueueRuleCredentialTemplates QueueRule = "credential-steps-run-one-template"
	// QueueRuleCredentialJobShape holds every job that may hold a credential
	// to allowed actions, plumbing templates and credential templates: no
	// general step, no service container, no working directory, an
	// environment of template env names only, and a handoff from the jobs it
	// needs through job outputs and artifacts alone.
	QueueRuleCredentialJobShape QueueRule = "credential-jobs-run-no-general-steps"
	// QueueRuleTokenMint requires every job that mints the releaser App token
	// to check that github.ref is the default branch, in a job-level or
	// mint-step if, and to mint only when tag mode is on. A dispatch on any
	// branch would otherwise run that branch's workflow file with the App key.
	QueueRuleTokenMint QueueRule = "token-mint-ref-check"
	// QueueRuleUnfiltered requires every workflow that owns a required context
	// to run on every pull_request and merge_group event: no paths,
	// paths-ignore, or branches-ignore filter that could skip the check the
	// queue waits on.
	QueueRuleUnfiltered QueueRule = "required-workflows-unfiltered"
	// QueueRuleMergeGate requires the aggregator job that always reports so a
	// change touching no heavy path is never stuck in the queue: it carries
	// if: always() and a needs list.
	QueueRuleMergeGate QueueRule = "merge-gate-always-reports"
	// QueueRuleTitles requires that no run or env block interpolates a PR
	// title or a commit message expression: titles are untrusted input.
	QueueRuleTitles QueueRule = "titles-never-interpolated"
	// QueueRuleCutRelease requires the cut-release job to trigger only on
	// workflow_dispatch, to take its level from a choice of exactly patch and
	// minor, and to write only when tag mode is on.
	QueueRuleCutRelease QueueRule = "cut-release-is-dispatch-only"
	// QueueRuleToolPin requires every checkout of the tool to name a 40-hex
	// commit, and all of them in one repo to name the same commit.
	QueueRuleToolPin QueueRule = "tool-pin-is-a-sha"
	// QueueRuleClassify requires workflows to call the shared classifier
	// instead of comparing a bot branch literal: no if or run compares == or
	// != against a configured bot branch, and the guard workflow invokes the
	// classifier to tell the release PR apart.
	QueueRuleClassify QueueRule = "bot-branch-via-classify"
	// QueueRuleToolCalls requires every release tool invocation in every
	// workflow to name an implemented command, to pass only flags that
	// command takes, and to pass every flag it requires, read from the shared
	// CLI vocabulary the tool parses its own arguments with. A call that
	// fails this fails at run time, after the check passed.
	QueueRuleToolCalls QueueRule = "tool-calls-match-the-cli"
)

// AllQueueRules is the canonical ordered closed set, used by generators and
// boundary validators.
var AllQueueRules = []QueueRule{
	QueueRuleRequiredContexts,
	QueueRuleMergeGroup,
	QueueRuleConcurrency,
	QueueRuleReleaseActions,
	QueueRuleTitleGuard,
	QueueRuleRangeFloor,
	QueueRuleTokenReadOnly,
	QueueRuleCredentialScope,
	QueueRuleCredentialActions,
	QueueRuleCredentialTemplates,
	QueueRuleCredentialJobShape,
	QueueRuleTokenMint,
	QueueRuleUnfiltered,
	QueueRuleMergeGate,
	QueueRuleTitles,
	QueueRuleCutRelease,
	QueueRuleToolPin,
	QueueRuleClassify,
	QueueRuleToolCalls,
}

// String renders the rule for diagnostic messages and CLI output.
func (r QueueRule) String() string { return string(r) }

// IsValid reports whether the rule is one of the known members.
func (r QueueRule) IsValid() bool { return slices.Contains(AllQueueRules, r) }

// ParseQueueRule maps a rule name to its member. Any other name is refused,
// never mapped to a nearby rule.
func ParseQueueRule(name string) (QueueRule, error) {
	return closedset.Parse(AllQueueRules, name, closedset.Context{
		Set:   "merge-queue rule",
		Where: "policy.ParseQueueRule",
		When:  "reading a rule name for a diagnostic or a test case",
		Means: "a rule name selects which check a diagnostic or a test case belongs to, so an unknown name selects none",
	})
}

// UnmarshalText parses a rule name at a decoding boundary, so a document
// that names a rule holds a member or fails to load.
func (r *QueueRule) UnmarshalText(text []byte) error {
	rule, err := ParseQueueRule(string(text))
	if err != nil {
		return err
	}
	*r = rule
	return nil
}

// Diagnostic is one merge-queue policy violation. Rule names the closed rule
// that fired; Workflow names the file carrying the fault (empty for a fault in
// the policy itself); Job names the offending job when the fault is scoped to
// one; Message states what is wrong, where, why it matters, and how to fix it.
type Diagnostic struct {
	Rule     QueueRule
	Workflow string
	Job      string
	Message  string
}

// RequiredContext is one required check the merge queue waits on: the context
// name the ruleset requires, the workflow file that reports it, and the job
// inside that file. Aggregator marks the job that always reports so a
// docs-only change is never stuck in the queue; Guard marks the title-guard
// job that must run the full check on every event with no early exit.
type RequiredContext struct {
	Context    string `yaml:"context"`
	Workflow   string `yaml:"workflow"`
	Job        string `yaml:"job"`
	Aggregator bool   `yaml:"aggregator"`
	Guard      bool   `yaml:"guard"`
}

// ReleaseJobRule locates the release job: the workflow file and job that carry
// out the one action the planner prints, and the ordered pre-tag step ids the
// canonical verdict reads. The release PR's head branch is not named here: it
// comes from the release config's bot_branches release entry, the one source,
// resolved by the checker.
type ReleaseJobRule struct {
	Workflow    string   `yaml:"workflow"`
	Job         string   `yaml:"job"`
	PreTagSteps []string `yaml:"pre_tag_steps"`
}

// BotJobRule locates one bot job that may push a branch: the workflow file,
// the job, and the bot kind whose branch it pushes. The branch itself is not
// named here: it comes from the release config's bot_branches entry for that
// kind, the one source, resolved by the checker.
type BotJobRule struct {
	Workflow string        `yaml:"workflow"`
	Job      string        `yaml:"job"`
	Kind     intent.PRKind `yaml:"kind"`
}

// botKinds lists the bot PR kinds a bot job may claim: every closed kind
// except ordinary, which is not a bot PR, and release, which belongs to the
// release job. It is derived from the intent set, so a new kind is admitted
// here by membership, never by a restated list.
func botKinds() []intent.PRKind {
	var out []intent.PRKind
	for _, k := range intent.AllPRKinds {
		if k != intent.PRKindOrdinary && k != intent.PRKindRelease {
			out = append(out, k)
		}
	}
	return out
}

// botKindValid reports whether kind is a bot PR kind a bot job may claim.
func botKindValid(kind intent.PRKind) bool {
	return slices.Contains(botKinds(), kind)
}

// QueueSection is the body of a repo's merge-queue policy. Required must be
// non-empty; ReleaseJob is absent only in a repo with no release job, and
// BotJobs lists every other job that may push a branch.
type QueueSection struct {
	Required   []RequiredContext `yaml:"required"`
	ReleaseJob *ReleaseJobRule   `yaml:"release_job"`
	BotJobs    []BotJobRule      `yaml:"bot_jobs"`
}

// validateQueueSection rejects a malformed queue section with an actionable
// error naming the entry and the fix. Semantic checks (a guard entry exists,
// one writer per bot branch, the workflow shapes) belong to the rule checker,
// which reports them as diagnostics; this only guards the structure a typo'd
// policy would otherwise silently disable.
func validateQueueSection(path string, queue QueueSection) error {
	prefix := "load queue policy: " + path + " queue"
	if len(queue.Required) == 0 {
		return fmt.Errorf("%s declares no required contexts during merge-queue policy validation. A queue section must list at least the title-guard context under 'required:'", prefix)
	}
	seen := make(map[string]bool, len(queue.Required))
	for i, req := range queue.Required {
		where := fmt.Sprintf("%s required[%d]", prefix, i)
		if req.Context == "" {
			return fmt.Errorf("%s has an empty context during merge-queue policy validation. Every required entry must set 'context:' to the check name the merge queue waits on", where)
		}
		if req.Workflow == "" {
			return fmt.Errorf("%s (context %q) has an empty workflow during merge-queue policy validation. Set 'workflow:' to the file in .github/workflows that reports it", where, req.Context)
		}
		if req.Job == "" {
			return fmt.Errorf("%s (context %q) has an empty job during merge-queue policy validation. Set 'job:' to the job in that file that reports the context", where, req.Context)
		}
		if seen[req.Context] {
			return fmt.Errorf("%s declares context %q twice during merge-queue policy validation. Each required context must appear once so the printed list compares exactly with the queue ruleset", where, req.Context)
		}
		seen[req.Context] = true
	}
	if queue.ReleaseJob != nil {
		where := prefix + " release_job"
		if queue.ReleaseJob.Workflow == "" {
			return fmt.Errorf("%s has an empty workflow during merge-queue policy validation. Set 'workflow:' to the release workflow file", where)
		}
		if queue.ReleaseJob.Job == "" {
			return fmt.Errorf("%s has an empty job during merge-queue policy validation. Set 'job:' to the release job that carries out the planner's action", where)
		}
		for i, step := range queue.ReleaseJob.PreTagSteps {
			if step == "" {
				return fmt.Errorf("%s pre_tag_steps[%d] is empty during merge-queue policy validation. Name the pre-tag step ids in verdict order, or drop the key when the repo has no pre-tag steps", where, i)
			}
		}
	}
	for i, bot := range queue.BotJobs {
		where := fmt.Sprintf("%s bot_jobs[%d]", prefix, i)
		if bot.Workflow == "" {
			return fmt.Errorf("%s has an empty workflow during merge-queue policy validation. Set 'workflow:' to the file holding the bot job", where)
		}
		if bot.Job == "" {
			return fmt.Errorf("%s has an empty job during merge-queue policy validation. Set 'job:' to the bot job that pushes its kind's branch", where)
		}
		if !botKindValid(bot.Kind) {
			return fmt.Errorf("%s names kind %q during merge-queue policy validation. A bot job claims one of the bot PR kinds (%s), so the checker can resolve its branch from the release config; the %s kind belongs to the release job", where, bot.Kind.String(), closedset.Join(botKinds(), ", "), intent.PRKindRelease.String())
		}
	}
	return nil
}
