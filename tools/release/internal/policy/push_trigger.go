package policy

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// pushTagCapability classifies the written trigger, not the publish job's if:
// without branch or tag filters GitHub push runs for both kinds of ref. Path
// filters do not apply to tag pushes. Unknown or malformed shapes are faults,
// not proof that a publishing job cannot run on a tag.
//
// The stricter tag-only contents-write exception also calls this classifier;
// it must not gain a write allowance from a malformed or ambiguous trigger.
func pushTagCapability(doc *yaml.Node) (bool, string) {
	if doc == nil || doc.Kind != yaml.MappingNode {
		return false, "the workflow is not a mapping"
	}
	var on *yaml.Node
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		if key.Value == "<<" {
			return false, "the workflow uses a YAML merge key"
		}
		if key.Value == workflowKeyOn {
			if on != nil || !triggerString(key) {
				return false, "the workflow has an ambiguous " + workflowKeyOn + " key"
			}
			on = doc.Content[i+1]
		}
	}
	if on == nil {
		return false, "the workflow has no " + workflowKeyOn + " trigger"
	}

	var push *yaml.Node
	pushFound := false
	seen := map[string]bool{}
	add := func(event, config *yaml.Node) string {
		if !triggerString(event) || !eventName(event.Value) {
			return "an event name is not a literal workflow event"
		}
		if seen[event.Value] {
			return "event " + strconv.Quote(event.Value) + " is repeated"
		}
		seen[event.Value] = true
		if parsed, err := ParseGitHubEvent(event.Value); err == nil && parsed == GitHubEventPush {
			pushFound = true
			push = config
		}
		return ""
	}
	switch on.Kind {
	case yaml.ScalarNode:
		if fault := add(on, nil); fault != "" {
			return false, fault
		}
	case yaml.SequenceNode:
		if len(on.Content) == 0 {
			return false, "the event list is empty"
		}
		for _, event := range on.Content {
			if fault := add(event, nil); fault != "" {
				return false, fault
			}
		}
	case yaml.MappingNode:
		if len(on.Content) == 0 {
			return false, "the event mapping is empty"
		}
		for i := 0; i+1 < len(on.Content); i += 2 {
			if fault := add(on.Content[i], on.Content[i+1]); fault != "" {
				return false, fault
			}
		}
	default:
		return false, "the trigger is neither an event name, a list nor a mapping"
	}
	if !pushFound {
		return false, ""
	}
	if push == nil || (push.Kind == yaml.ScalarNode && push.Tag == "!!null") {
		return true, ""
	}
	if push.Kind != yaml.MappingNode {
		return false, GitHubEventPush.String() + " configuration is neither null nor a filter mapping"
	}

	filters := map[string]bool{}
	for i := 0; i+1 < len(push.Content); i += 2 {
		key, value := push.Content[i], push.Content[i+1]
		if !triggerString(key) {
			return false, "a push filter name is not a literal string"
		}
		name := key.Value
		switch name {
		case triggerKeyTags, triggerKeyTagsIgnore, triggerKeyBranches, triggerKeyBranchesIgnore, triggerKeyPaths, triggerKeyPathsIgnore:
		default:
			return false, "unknown push filter " + strconv.Quote(name)
		}
		if filters[name] {
			return false, "push filter " + strconv.Quote(name) + " is repeated"
		}
		filters[name] = true
		if !triggerPatterns(value) {
			return false, "push filter " + strconv.Quote(name) + " is not a nonempty literal pattern or list of patterns"
		}
	}
	if filters[triggerKeyTags] && filters[triggerKeyTagsIgnore] ||
		filters[triggerKeyBranches] && filters[triggerKeyBranchesIgnore] ||
		filters[triggerKeyPaths] && filters[triggerKeyPathsIgnore] {
		return false, "push sets both inclusive and exclusive filters for the same ref or path"
	}
	return filters[triggerKeyTags] || filters[triggerKeyTagsIgnore] ||
		!(filters[triggerKeyBranches] || filters[triggerKeyBranchesIgnore]), ""
}

func triggerString(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!str" && strings.TrimSpace(node.Value) != ""
}

// eventName permits unknown GitHub events without treating arbitrary YAML or
// expressions as proof that a push trigger is absent.
func eventName(name string) bool {
	for i, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || i > 0 && (ch >= '0' && ch <= '9' || ch == '_')) {
			return false
		}
	}
	return name != ""
}

func triggerPatterns(value *yaml.Node) bool {
	var patterns []*yaml.Node
	if triggerString(value) {
		patterns = []*yaml.Node{value}
	} else if value != nil && value.Kind == yaml.SequenceNode && len(value.Content) > 0 {
		patterns = value.Content
	} else {
		return false
	}
	positive := false
	for _, item := range patterns {
		if !triggerString(item) || strings.Contains(item.Value, "${{") {
			return false
		}
		if strings.HasPrefix(item.Value, "!") {
			if item.Value == "!" {
				return false
			}
		} else {
			positive = true
		}
	}
	// GitHub's ordered negative patterns supplement at least one positive
	// pattern; a negation-only filter is not a proven branch-only trigger.
	return positive
}
