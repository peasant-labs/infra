package policy

import (
	"path"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// This file is the tool-calls-match-the-cli rule: every release tool
// invocation in every workflow names an implemented command, passes only
// flags that command takes, and passes every flag it requires. The command
// set and each command's flags come from the shared CLI vocabulary, the same
// owner the tool parses its own arguments with, so a call the check accepts
// is a call the tool accepts.

// toolCalls returns the release tool invocations in a run block: every
// simple command, including those in substitutions, whose command word is
// the tool. A build or a copy that names the binary as an argument is not an
// invocation. complete is false when the block does not lex completely.
func toolCalls(run string) ([]shellCommand, bool) {
	cmds, complete := shellCommands(run)
	var out []shellCommand
	for _, cmd := range cmds {
		at := commandWord(cmd)
		if at < 0 || path.Base(cmd[at].bare) != intent.ProgramName {
			continue
		}
		out = append(out, cmd[at:])
	}
	return out, complete
}

// toolCallProblems checks one invocation (its first word is the tool) against
// the CLI vocabulary and returns what the tool would refuse: a missing or
// unknown command, an unknown flag, a flag the command does not take, and
// each required flag the call does not pass. Every word that starts with '-'
// is a flag: the tool refuses a flag-like value, so no value starts with '-'.
func toolCallProblems(call shellCommand) []string {
	if len(call) < 2 {
		return []string{"missing command"}
	}
	command, err := intent.ParseCommand(call[1].bare)
	if err != nil {
		return []string{"unknown command " + strconv.Quote(call[1].bare)}
	}
	var problems []string
	passed := map[intent.Flag]bool{}
	for _, w := range call[2:] {
		if !strings.HasPrefix(w.bare, "-") {
			continue
		}
		flag, err := intent.ParseFlag(w.bare)
		switch {
		case err != nil:
			problems = append(problems, "unknown flag "+strconv.Quote(w.bare))
		case !command.Accepts(flag):
			problems = append(problems, "flag "+strconv.Quote(flag.String())+" does not apply to "+command.String())
		default:
			passed[flag] = true
		}
	}
	for _, flag := range command.RequiredFlags() {
		if !passed[flag] {
			problems = append(problems, "missing required flag "+strconv.Quote(flag.String()))
		}
	}
	return problems
}

// checkToolCalls (tool-calls-match-the-cli) applies toolCallProblems to every
// run step of every workflow, so a call that would fail at run time is
// rejected when the workflows are checked.
func (c *queueChecker) checkToolCalls() {
	c.eachDoc(QueueRuleToolCalls, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			job := jobNode(doc, jobName)
			for _, step := range stepsOf(job) {
				if shell := effectiveShell(step, job, doc); shell != "" && !posixShell(shell) {
					continue
				}
				calls, _ := toolCalls(stepRunText(step))
				for _, call := range calls {
					for _, problem := range toolCallProblems(call) {
						c.emit(QueueRuleToolCalls, name, stepID(step),
							"job "+strconv.Quote(jobName)+" runs "+strconv.Quote(strings.Join(call.bareWords(), " "))+" with "+problem+" during merge-queue policy validation. "+toolCallFix(call))
					}
				}
			}
		}
	})
}

// toolCallFix says how to call the tool: the command set, or the named
// command's accepted and required flags, all from the CLI vocabulary.
func toolCallFix(call shellCommand) string {
	if len(call) < 2 {
		return "Name one of " + closedset.Join(intent.AllCommands, ", ") + "; the tool refuses the call at run time otherwise"
	}
	command, err := intent.ParseCommand(call[1].bare)
	if err != nil {
		return "Name one of " + closedset.Join(intent.AllCommands, ", ") + "; the tool refuses the call at run time otherwise"
	}
	var accepted []intent.Flag
	for _, use := range command.Flags() {
		accepted = append(accepted, use.Flag)
	}
	fix := command.String() + " takes " + joinOrNone(accepted)
	fix += " and requires " + joinOrNone(command.RequiredFlags())
	return fix + ", each flag and its value as separate words; the tool refuses the call at run time otherwise"
}

func joinOrNone(flags []intent.Flag) string {
	if len(flags) == 0 {
		return "no flags"
	}
	return closedset.Join(flags, ", ")
}
