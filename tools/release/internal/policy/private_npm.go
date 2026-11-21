package policy

import (
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file implements npm-installs-use-private-prefix: every run step of
// every workflow in --dir is scanned by the package's non-executing POSIX
// shell command scanner (scanCommands) so each simple command is evaluated in
// isolation. Global npm installs race on the shared runner tool cache across
// concurrent VMs; callers must install into a job-private prefix under
// $RUNNER_TEMP and add its bin directory to GITHUB_PATH.

var npmInstallVerbs = map[string]bool{
	"install": true,
	"i":       true,
	"add":     true,
	"in":      true,
	"ins":     true,
	"inst":    true,
	"insta":   true,
	"instal":  true,
}

// npmValueFlags are npm options whose two-word form consumes the following
// word as a value rather than a subcommand or flag.
var npmValueFlags = map[string]bool{
	"--location":     true,
	"--prefix":       true,
	"-C":             true,
	"--registry":     true,
	"--cache":        true,
	"--userconfig":   true,
	"--globalconfig": true,
	"--workspace":    true,
	"-w":             true,
	"--loglevel":     true,
	"--tag":          true,
	"--otp":          true,
	"--cpu":          true,
	"--os":           true,
	"--libc":         true,
}

func isExplicitNpmFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "false", "0", "no":
		return true
	}
	return false
}

// globalNpmInstallMatch reports whether a POSIX shell run block invokes an npm
// install verb globally. It reuses scanCommands/commandWord so quotes,
// escapes, line continuations, comments, command separators, heredocs, and
// command substitutions are handled without executing the shell text.
func globalNpmInstallMatch(run string) bool {
	normalized := strings.ReplaceAll(run, "\r\n", "\n")
	for _, cmd := range scanCommands(normalized) {
		if isGlobalNpmCommand(cmd) {
			return true
		}
	}
	return false
}

func isGlobalNpmCommand(cmd shellCommand) bool {
	at := commandWord(cmd)
	if at >= 0 && len(cmd) > at+2 && cmd[at].bare == "nix" && cmd[at+1].bare == "develop" {
		switch {
		case cmd[at+2].bare == "-c":
			at += 3
		case len(cmd) > at+3 && cmd[at+3].bare == "-c" && !strings.HasPrefix(cmd[at+2].bare, "-"):
			at += 4
		}
	}
	if at < 0 || at >= len(cmd) || path.Base(cmd[at].bare) != "npm" {
		return false
	}

	// npm uses global mode if either setting requests it. Setting location
	// to project does not clear --global, and --no-global does not clear
	// --location=global.
	global, globalLocation := false, false
	for _, w := range cmd[:at] {
		name, val, ok := strings.Cut(w.bare, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(name) {
		case "npm_config_global":
			global = !isExplicitNpmFalse(val)
		case "npm_config_location":
			globalLocation = val == "global"
		}
	}

	verb := ""
	args := cmd[at+1:]
	for i := 0; i < len(args); i++ {
		a := args[i].bare
		if a == "--" {
			break
		}
		switch {
		case a == "-g" || a == "--global":
			global = true
			if i+1 < len(args) {
				switch args[i+1].bare {
				case "true", "false":
					i++
					global = args[i].bare == "true"
				}
			}
		case a == "--no-global":
			global = false
		case strings.HasPrefix(a, "--global=") || strings.HasPrefix(a, "-g="):
			_, value, _ := strings.Cut(a, "=")
			global = !isExplicitNpmFalse(value)
		case a == "--location":
			if i+1 < len(args) {
				globalLocation = args[i+1].bare == "global"
				i++
			}
		case strings.HasPrefix(a, "--location="):
			globalLocation = strings.TrimPrefix(a, "--location=") == "global"
		case npmValueFlags[a]:
			if i+1 < len(args) {
				i++
			}
		case strings.HasPrefix(a, "-"):
			// Other flag (including --flag=value).
		default:
			if verb == "" {
				verb = a
			}
		}
	}
	return npmInstallVerbs[verb] && (global || globalLocation)
}

func (c *queueChecker) checkPrivateNpm() {
	c.eachDoc(QueueRulePrivateNpm, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			job := jobNode(doc, jobName)
			for _, step := range stepsOf(job) {
				if shell := effectiveShell(step, job, doc); shell != "" && !posixShell(shell) {
					continue
				}
				if globalNpmInstallMatch(stepRunText(step)) {
					c.emit(QueueRulePrivateNpm, name, jobName, "step "+stepID(step)+" runs a global npm install; the shared runner tool cache races across concurrent VMs. Install into a job-private prefix under $RUNNER_TEMP and add its bin to GITHUB_PATH")
				}
			}
		}
	})
}
