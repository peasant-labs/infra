package main

import (
	"fmt"
	"strings"
)

// Command is the closed set of unstring's commands.
type Command string

const (
	// CommandCheck compares the tree with the baseline.
	CommandCheck Command = "check"
	// CommandUpdate rewrites the baseline; it never raises a count.
	CommandUpdate Command = "update"
	// CommandInit writes the first baseline when none exists.
	CommandInit Command = "init"
	// CommandShow prints one read-only topic.
	CommandShow Command = "show"
)

// AllCommands lists every Command in the order the usage prints them.
var AllCommands = []Command{CommandCheck, CommandUpdate, CommandInit, CommandShow}

// String returns the command as typed on the command line.
func (c Command) String() string { return string(c) }

// IsValid reports whether c is a member of AllCommands.
func (c Command) IsValid() bool {
	switch c {
	case CommandCheck, CommandUpdate, CommandInit, CommandShow:
		return true
	}
	return false
}

// ParseCommand converts a command-line word into a Command.
func ParseCommand(s string) (Command, error) {
	if c := Command(s); c.IsValid() {
		return c, nil
	}
	return "", fmt.Errorf("unknown command %q", s)
}

// ShowTopic is the closed set of topics `unstring show` prints.
type ShowTopic string

const (
	// TopicCatalog prints the generated catalog: members, source kinds and
	// definition sites.
	TopicCatalog ShowTopic = "catalog"
	// TopicRules prints every rule id with its language, message and fix.
	TopicRules ShowTopic = "rules"
	// TopicVersion prints the tool version and the ast-grep version it
	// requires.
	TopicVersion ShowTopic = "version"
)

// AllShowTopics lists every ShowTopic in the order the usage prints them.
var AllShowTopics = []ShowTopic{TopicCatalog, TopicRules, TopicVersion}

// String returns the topic as typed on the command line.
func (t ShowTopic) String() string { return string(t) }

// IsValid reports whether t is a member of AllShowTopics.
func (t ShowTopic) IsValid() bool {
	switch t {
	case TopicCatalog, TopicRules, TopicVersion:
		return true
	}
	return false
}

// ParseShowTopic converts a command-line word into a ShowTopic.
func ParseShowTopic(s string) (ShowTopic, error) {
	if t := ShowTopic(s); t.IsValid() {
		return t, nil
	}
	return "", fmt.Errorf("unknown topic %q", s)
}

func joinNames[T fmt.Stringer](values []T, sep string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = v.String()
	}
	return strings.Join(parts, sep)
}
