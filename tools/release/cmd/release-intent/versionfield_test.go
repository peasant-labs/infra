package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// hostileVersionDocument builds a version document that is deliberately
// hostile to whole-file rewrites: unsorted keys, scripts with shell
// operators the JSON escaper would mangle, and the protected field, named by
// the config, in the middle.
func hostileVersionDocument(vf intent.VersionFile, version intent.SemVer) string {
	return `{"name": "fairtrade",
  "scripts": {"lint": "a && b > c", "build": "x || y"},
  ` + strconv.Quote(vf.Field) + `: ` + strconv.Quote(version.String()) + `,
  "dependencies": {"left-pad": "1.3.0"}
}
`
}

// hostileScriptsLine is the line of the hostile document a whole-file
// rewrite would re-escape.
const hostileScriptsLine = `"lint": "a && b > c"`

// TestWriteReleaseContent_OnlyVersionLineChanges proves writeReleaseContent
// splices only the protected version value of a hostile version document. It
// runs in the repo dir (t.Chdir, restored automatically) because
// writeReleaseContent resolves repo-relative paths like the binary does when
// run from the repository root.
func TestWriteReleaseContent_OnlyVersionLineChanges(t *testing.T) {
	r := newTestRepo(t, repoStandard)
	r.Seed()
	cfg := r.Config()
	vf := cfg.VersionFiles[0]
	r.WriteVersionDocument(0, hostileVersionDocument(vf, r.fix.SeedVersion))
	r.Commit(titleSeed)
	next := laterVersion(t, r, intent.LevelPatch)
	content := intent.Content{
		Changelog: r.ReadChangelog(),
		Versions:  map[string]string{vf.Path: next.String()},
	}
	t.Chdir(r.Dir())
	if err := writeReleaseContent(cfg, content); err != nil {
		t.Fatalf("writeReleaseContent: %v", err)
	}
	got := r.ReadVersionDocument(0)
	if got != hostileVersionDocument(vf, next) {
		t.Fatalf("rewritten version document differs from the original beyond the version value:\n%s", got)
	}
	if !strings.Contains(got, hostileScriptsLine) {
		t.Fatalf("scripts line changed, want byte-identical:\n%s", got)
	}
	if value := r.ReadVersionField(0); value != next.String() {
		t.Fatalf("protected field = %q, want %q", value, next)
	}
	if changed := r.ChangedLines(0); changed != "1 added, 1 deleted" {
		t.Fatalf("version document diff = %q, want one added and one deleted line", changed)
	}
}
