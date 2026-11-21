package catalog_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/unstring/internal/catalog"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

func goBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is not on PATH: %v. Fix: run the tests from the repository dev shell, which provides go.", err)
	}
	return p
}

// The production resolver runs `go mod download -json`; here it reads a
// GOPROXY=file:// tree built from the committed plain-source fixture module,
// so the test makes no network call and leaves a removable module cache.
func TestGoModDownloadResolvesFromTheFileProxy(t *testing.T) {
	proxy := buildProxy(t, schemaFixtureDir, schemaFixtureModule, schemaFixtureVersion)
	env := offlineGoEnv(t, proxy)
	dir, err := catalog.GoModDownload{GoCommand: goBinary(t)}.Resolve(context.Background(), schemaFixtureModule, schemaFixtureVersion, env)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(schemaFixtureDir, "visibility.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "visibility.go"))
	if err != nil {
		t.Fatalf("the resolved directory %s does not hold the module's source: %v", dir, err)
	}
	if string(got) != string(want) {
		t.Errorf("the resolved visibility.go differs from the fixture module's")
	}
}

func TestGoModDownloadFailures(t *testing.T) {
	proxy := buildProxy(t, schemaFixtureDir, schemaFixtureModule, schemaFixtureVersion)
	empty := t.TempDir()
	for _, tc := range []struct {
		name, proxy, module, version string
	}{
		{"version-not-published", proxy, schemaFixtureModule, "v0.0.2"},
		{"module-not-in-proxy", proxy, "example.test/absent", "v0.0.1"},
		{"empty-proxy", empty, schemaFixtureModule, schemaFixtureVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := catalog.GoModDownload{GoCommand: goBinary(t)}.Resolve(context.Background(), tc.module, tc.version, offlineGoEnv(t, tc.proxy))
			var be *catalog.BuildError
			if !errors.As(err, &be) {
				t.Fatalf("Resolve error = %v; want a *catalog.BuildError", err)
			}
			if !strings.Contains(be.What, "could not be downloaded") || !strings.Contains(be.Fix, "GOPROXY=file://") {
				t.Errorf("BuildError = %q / fix %q; want the download failure naming the proxy", be.What, be.Fix)
			}
		})
	}
}

// The whole build through the production resolver: a Go repository pinning
// the fixture module in go.mod gets the module's members.
func TestBuildWithGoModDownload(t *testing.T) {
	proxy := buildProxy(t, schemaFixtureDir, schemaFixtureModule, schemaFixtureVersion)
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod":  "module example.test/app\n\nrequire example.test/schema v0.0.1\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	b := catalog.NewBuilder(catalog.BuilderDeps{Runner: newRunner(t), Modules: catalog.GoModDownload{GoCommand: goBinary(t)}})
	c, err := b.Build(context.Background(), catalog.BuildRequest{
		Dir: dir, Lang: rules.LangGo, Roots: []string{"."}, SchemaModule: schemaFixtureModule, Env: offlineGoEnv(t, proxy),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := c.Values()
	for _, want := range []string{"public", "private", "owner", "reader", "GET", "POST", "queued", "shared", "alpha", "beta"} {
		found := false
		for _, v := range got {
			found = found || v == want
		}
		if !found {
			t.Errorf("catalog values %v lack the schema member %q", got, want)
		}
	}
	for _, m := range c.Members {
		if m.Site.Module != schemaFixtureModule+"@"+schemaFixtureVersion {
			t.Errorf("member %q has module %q, want the pinned fixture module", m.Value, m.Site.Module)
		}
	}
}

// An exclude predicate filters the scanned repository only: even one that
// excludes every path, including the module-relative paths of the schema's
// own files, leaves the pinned schema module's members in the catalog.
func TestBuildExclusionNeverFiltersSchemaMembers(t *testing.T) {
	proxy := buildProxy(t, schemaFixtureDir, schemaFixtureModule, schemaFixtureVersion)
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod":  "module example.test/app\n\nrequire example.test/schema v0.0.1\n",
		"main.go": "package main\n\ntype Mode string\n\nconst (\n\tA Mode = \"repo-a\"\n\tB Mode = \"repo-b\"\n)\n",
	})
	b := catalog.NewBuilder(catalog.BuilderDeps{Runner: newRunner(t), Modules: catalog.GoModDownload{GoCommand: goBinary(t)}})
	c, err := b.Build(context.Background(), catalog.BuildRequest{
		Dir: dir, Lang: rules.LangGo, Roots: []string{"."}, SchemaModule: schemaFixtureModule, Env: offlineGoEnv(t, proxy),
		Excluded: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := c.Values()
	has := func(v string) bool {
		for _, g := range got {
			if g == v {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"public", "private", "queued", "shared"} {
		if !has(want) {
			t.Errorf("catalog values %v lack the schema member %q", got, want)
		}
	}
	for _, gone := range []string{"repo-a", "repo-b"} {
		if has(gone) {
			t.Errorf("catalog values %v keep the excluded repository member %q", got, gone)
		}
	}
}
