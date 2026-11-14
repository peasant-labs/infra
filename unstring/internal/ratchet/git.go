package ratchet

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ReadGitState asks git for the renames update may follow: every rename
// between the commit that last changed the baseline file and the working
// tree (index included, git's default similarity), and the untracked files.
// dir is the config directory; paths come back relative to it. Outside a git
// work tree, or before the first commit, there is nothing to follow and the
// state is empty. Only update calls this; check never uses git.
func ReadGitState(ctx context.Context, dir, baselinePath string) (GitState, error) {
	state := GitState{Renames: map[string]string{}, Untracked: map[string]bool{}}
	if _, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return state, nil
	}
	base, err := git(ctx, dir, "log", "-1", "--format=%H", "--", baselinePath)
	if err != nil {
		return state, err
	}
	commit := strings.TrimSpace(string(base))
	if commit == "" {
		head, err := git(ctx, dir, "rev-parse", "--verify", "-q", "HEAD")
		if err != nil {
			// No commit yet: no rename can be confirmed.
			return state, nil
		}
		commit = strings.TrimSpace(string(head))
	}

	diff, err := git(ctx, dir, "diff", "-z", "--name-status", "-M", "--relative", commit)
	if err != nil {
		return state, err
	}
	fields := strings.Split(strings.TrimSuffix(string(diff), "\x00"), "\x00")
	for i := 0; i < len(fields); {
		status := fields[i]
		if status == "" {
			i++
			continue
		}
		if status[0] == 'R' || status[0] == 'C' {
			if i+2 >= len(fields) {
				break
			}
			if status[0] == 'R' {
				state.Renames[fields[i+2]] = fields[i+1]
			}
			i += 3
			continue
		}
		i += 2
	}

	untracked, err := git(ctx, dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return state, err
	}
	for _, f := range strings.Split(string(untracked), "\x00") {
		if f != "" {
			state.Untracked[f] = true
		}
	}
	return state, nil
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotePath=false"}, args...)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
