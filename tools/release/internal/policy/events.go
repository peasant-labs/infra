package policy

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// GitHubEvent names one workflow trigger the queue rules decide on. The set
// is closed over what the rules read, not over everything GitHub can send: a
// workflow may trigger on any other event, and that trigger is kept by its
// written name with the zero event, which matches no member and so satisfies
// no rule.
type GitHubEvent string

const (
	// GitHubEventPullRequest is the event a pull-request head runs under.
	GitHubEventPullRequest GitHubEvent = "pull_request"
	// GitHubEventMergeGroup is the event a merge-queue group build runs under.
	GitHubEventMergeGroup GitHubEvent = "merge_group"
	// GitHubEventPush is a pushed branch or tag.
	GitHubEventPush GitHubEvent = "push"
	// GitHubEventWorkflowDispatch is a manual run from the button or the API.
	GitHubEventWorkflowDispatch GitHubEvent = "workflow_dispatch"
)

// AllGitHubEvents is the canonical ordered closed set of the events the
// queue rules decide on.
var AllGitHubEvents = []GitHubEvent{
	GitHubEventPullRequest,
	GitHubEventMergeGroup,
	GitHubEventPush,
	GitHubEventWorkflowDispatch,
}

// String renders the event in its workflow spelling.
func (e GitHubEvent) String() string { return string(e) }

// IsValid reports whether the event is one of the known members. The zero
// value is not.
func (e GitHubEvent) IsValid() bool {
	for _, known := range AllGitHubEvents {
		if e == known {
			return true
		}
	}
	return false
}

// ParseGitHubEvent maps a workflow trigger name to its member. Any other
// name is refused, never mapped to a nearby member.
func ParseGitHubEvent(name string) (GitHubEvent, error) {
	if e := GitHubEvent(name); e.IsValid() {
		return e, nil
	}
	return "", fmt.Errorf("parse workflow trigger: %q is not one of the events the merge-queue rules read (%s). A caller keeps such a trigger by its written name as unknown, and it satisfies no rule; write one of the listed names if a rule was meant to see it", name, joinMembers(AllGitHubEvents))
}

// PullRequestAction names one pull_request activity type the queue rules
// decide on: the three GitHub runs by default, and the edit that changes a
// title without changing the head commit. Any other activity type in a
// 'types:' list is kept by its written name with the zero action, which
// matches no member.
type PullRequestAction string

const (
	// PullRequestActionOpened fires when the pull request is opened.
	PullRequestActionOpened PullRequestAction = "opened"
	// PullRequestActionSynchronize fires when the head branch moves.
	PullRequestActionSynchronize PullRequestAction = "synchronize"
	// PullRequestActionReopened fires when a closed pull request is reopened.
	PullRequestActionReopened PullRequestAction = "reopened"
	// PullRequestActionEdited fires when the title, body or base is edited.
	PullRequestActionEdited PullRequestAction = "edited"
)

// AllPullRequestActions is the canonical ordered closed set of the activity
// types the queue rules decide on.
var AllPullRequestActions = []PullRequestAction{
	PullRequestActionOpened,
	PullRequestActionSynchronize,
	PullRequestActionReopened,
	PullRequestActionEdited,
}

// DefaultPullRequestActions is the set GitHub runs a pull_request workflow
// on when the trigger names no 'types:'. A required workflow that names
// types must keep all of them, or a head change skips the check the queue
// waits on.
var DefaultPullRequestActions = []PullRequestAction{
	PullRequestActionOpened,
	PullRequestActionSynchronize,
	PullRequestActionReopened,
}

// String renders the action in its workflow spelling.
func (a PullRequestAction) String() string { return string(a) }

// IsValid reports whether the action is one of the known members. The zero
// value is not.
func (a PullRequestAction) IsValid() bool {
	for _, known := range AllPullRequestActions {
		if a == known {
			return true
		}
	}
	return false
}

// ParsePullRequestAction maps an activity type to its member. Any other name
// is refused, never mapped to a nearby member.
func ParsePullRequestAction(name string) (PullRequestAction, error) {
	if a := PullRequestAction(name); a.IsValid() {
		return a, nil
	}
	return "", fmt.Errorf("parse %s activity type: %q is not one of the activity types the merge-queue rules read (%s). A caller keeps such a type by its written name as unknown, and it stands in for no member; write one of the listed names if a rule was meant to see it", GitHubEventPullRequest, name, joinMembers(AllPullRequestActions))
}

// joinMembers renders a closed set's members for a message, in canonical
// order.
func joinMembers[T fmt.Stringer](members []T) string {
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.String())
	}
	return strings.Join(names, ", ")
}

// trigger is one entry of a workflow's 'on:' parsed at the YAML boundary.
// Event is the zero value when the written name is not a member; Name keeps
// the spelling either way. Config is the trigger's own mapping, nil in the
// scalar and sequence forms and for an empty mapping value.
type trigger struct {
	Event  GitHubEvent
	Name   string
	Config *yaml.Node
}

// workflowTriggers parses the workflow's 'on:' in the three forms GitHub
// accepts: one event name, a sequence of names, or a mapping of name to
// configuration. It is the one place a trigger name becomes a GitHubEvent;
// every rule reads the result.
func workflowTriggers(doc *yaml.Node) []trigger {
	on := mappingValue(doc, workflowKeyOn)
	if on == nil {
		return nil
	}
	parse := func(name string, config *yaml.Node) trigger {
		// A name outside the set keeps the zero event: unknown, never coerced.
		event, _ := ParseGitHubEvent(name)
		if config != nil && config.Kind != yaml.MappingNode {
			config = nil
		}
		return trigger{Event: event, Name: name, Config: config}
	}
	var out []trigger
	switch on.Kind {
	case yaml.ScalarNode:
		out = append(out, parse(on.Value, nil))
	case yaml.SequenceNode:
		for _, item := range on.Content {
			out = append(out, parse(item.Value, nil))
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			out = append(out, parse(on.Content[i].Value, on.Content[i+1]))
		}
	}
	return out
}

// findTrigger returns the workflow's trigger for event, or false.
func findTrigger(doc *yaml.Node, event GitHubEvent) (trigger, bool) {
	for _, t := range workflowTriggers(doc) {
		if t.Event == event {
			return t, true
		}
	}
	return trigger{}, false
}

// hasTrigger reports whether the workflow's 'on:' includes the event.
func hasTrigger(doc *yaml.Node, event GitHubEvent) bool {
	_, ok := findTrigger(doc, event)
	return ok
}

// soleTrigger returns the workflow's one trigger when 'on:' names exactly one
// event and it is the given member.
func soleTrigger(doc *yaml.Node, event GitHubEvent) (trigger, bool) {
	triggers := workflowTriggers(doc)
	if len(triggers) != 1 || triggers[0].Event != event {
		return trigger{}, false
	}
	return triggers[0], true
}

// activityTypes parses a pull_request trigger's 'types:' at the YAML
// boundary. listed is false when the trigger names no types, which means
// GitHub's defaults apply. A name outside the set contributes nothing to
// the result: it is unknown and can stand in for no member.
func activityTypes(t trigger) (actions []PullRequestAction, listed bool) {
	types := mappingValue(t.Config, triggerKeyTypes)
	if types == nil {
		return nil, false
	}
	var names []string
	switch types.Kind {
	case yaml.ScalarNode:
		names = []string{types.Value}
	case yaml.SequenceNode:
		for _, item := range types.Content {
			names = append(names, item.Value)
		}
	}
	for _, name := range names {
		if action, err := ParsePullRequestAction(name); err == nil {
			actions = append(actions, action)
		}
	}
	return actions, true
}

// Workflow syntax keys the checker reads. They are GitHub's spelling, named
// once so every rule reads the same key.
const (
	workflowKeyOn   = "on"
	triggerKeyTypes = "types"
	triggerKeyTags  = "tags"
)

// triggerFilterKeys are the trigger keys that can skip a run for some
// changes: path filters and the branch exclusion.
var triggerFilterKeys = []string{"paths", "paths-ignore", "branches-ignore"}
