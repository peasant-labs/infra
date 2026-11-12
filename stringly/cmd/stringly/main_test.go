package main

import (
	"bytes"
	_ "embed"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

//go:embed testdata/cli.yaml
var cliYAML []byte

type cliCase struct {
	Name       string   `yaml:"name"`
	Args       []string `yaml:"args"`
	Linked     string   `yaml:"linked"`
	Built      string   `yaml:"built"`
	DevEnv     string   `yaml:"devEnv"`
	WantExit   int      `yaml:"wantExit"`
	WantStdout string   `yaml:"wantStdout"`
	WantStderr []string `yaml:"wantStderr"`
}

type cliFixture struct {
	Commands   []string  `yaml:"commands"`
	ShowTopics []string  `yaml:"showTopics"`
	Invalid    []string  `yaml:"invalidWords"`
	Cases      []cliCase `yaml:"cases"`
}

// The cases every change to the entry point must keep.
var requiredCLICases = []string{
	"show-version-prints-linked-version",
	"show-version-prints-build-info-version",
	"linked-version-overrides-build-info",
	"build-info-pseudo-version-is-a-version",
	"devel-build-runs-with-dev-env",
	"devel-build-refused-without-dev-env",
	"dirty-build-refused-without-dev-env",
	"no-version-refused-without-dev-env",
	"dev-env-other-than-one-is-refused",
	"malformed-linked-version-refused",
	"no-command-lists-commands",
	"unknown-command-lists-commands",
	"show-without-topic-lists-topics",
	"show-unknown-topic-lists-topics",
	"show-version-refuses-extra-argument",
}

func loadCLIFixture(t *testing.T) cliFixture {
	t.Helper()
	var f cliFixture
	dec := yaml.NewDecoder(bytes.NewReader(cliYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/cli.yaml: %v", err)
	}
	return f
}

func TestRequiredCLICasesExist(t *testing.T) {
	f := loadCLIFixture(t)
	var names []string
	for _, c := range f.Cases {
		if slices.Contains(names, c.Name) {
			t.Errorf("testdata/cli.yaml has two cases named %q", c.Name)
		}
		names = append(names, c.Name)
	}
	for _, want := range requiredCLICases {
		if !slices.Contains(names, want) {
			t.Errorf("testdata/cli.yaml is missing required case %q", want)
		}
	}
}

func TestCLI(t *testing.T) {
	for _, c := range loadCLIFixture(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			getenv := func(key string) string {
				if key == devEnvVar {
					return c.DevEnv
				}
				return ""
			}
			code := run(c.Args, c.Linked, c.Built, getenv, &stdout, &stderr)
			if code != c.WantExit {
				t.Errorf("exit %d, want %d\nstderr:\n%s", code, c.WantExit, stderr.String())
			}
			if got := stdout.String(); got != c.WantStdout {
				t.Errorf("stdout %q, want %q", got, c.WantStdout)
			}
			errText := stderr.String()
			for _, want := range c.WantStderr {
				if !strings.Contains(errText, want) {
					t.Errorf("stderr does not contain %q:\n%s", want, errText)
				}
			}
			if c.WantExit != exitClean {
				for _, part := range []string{"\n  why    ", "\n  where  ", "\n  fix    "} {
					if !strings.Contains(errText, part) {
						t.Errorf("failure output lacks the %q part of the message contract:\n%s", strings.TrimSpace(part), errText)
					}
				}
			} else if errText != "" {
				t.Errorf("a clean run wrote to stderr:\n%s", errText)
			}
		})
	}
}

func TestCommandAndTopicMembership(t *testing.T) {
	f := loadCLIFixture(t)
	assertNames(t, "AllCommands", AllCommands, f.Commands, ParseCommand)
	assertNames(t, "AllShowTopics", AllShowTopics, f.ShowTopics, ParseShowTopic)
	for _, bad := range f.Invalid {
		if _, err := ParseCommand(bad); err == nil {
			t.Errorf("ParseCommand(%q) succeeded, want an error", bad)
		}
		if _, err := ParseShowTopic(bad); err == nil {
			t.Errorf("ParseShowTopic(%q) succeeded, want an error", bad)
		}
	}
}

func assertNames[T interface {
	~string
	String() string
	IsValid() bool
}](t *testing.T, name string, all []T, want []string, parse func(string) (T, error)) {
	t.Helper()
	var got []string
	for _, v := range all {
		if slices.Contains(got, v.String()) {
			t.Errorf("%s lists %q twice", name, v)
		}
		got = append(got, v.String())
		if p, err := parse(v.String()); err != nil || p != v || !v.IsValid() {
			t.Errorf("%s member %q does not round-trip: %q, %v", name, v, p, err)
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Errorf("%s = %v, want exactly %v (testdata/cli.yaml)", name, got, want)
	}
}
