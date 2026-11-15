package ratchet_test

import (
	"bytes"
	_ "embed"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/unstring/internal/ratchet"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

//go:embed testdata/report.yaml
var reportYAML []byte

type reportCase struct {
	Name     string `yaml:"name"`
	Format   string `yaml:"format"`
	Command  string `yaml:"command"`
	Findings []struct {
		Kind  string `yaml:"kind"`
		Rule  string `yaml:"rule"`
		File  string `yaml:"file"`
		Line  int    `yaml:"line"`
		Text  string `yaml:"text"`
		What  string `yaml:"what"`
		Why   string `yaml:"why"`
		Where string `yaml:"where"`
		Fix   string `yaml:"fix"`
	} `yaml:"findings"`
	Want string `yaml:"want"`
}

var requiredReportCases = []string{
	"text-hit-finding",
	"text-finding-without-rule",
	"text-no-findings",
	"github-annotation-escapes",
}

func TestWriteReport(t *testing.T) {
	f := loadReportFixture(t)
	var names []string
	for _, c := range f.Cases {
		names = append(names, c.Name)
		t.Run(c.Name, func(t *testing.T) {
			format, err := ratchet.ParseFormat(c.Format)
			if err != nil {
				t.Fatal(err)
			}
			var fs []ratchet.Finding
			for _, x := range c.Findings {
				kind, err := ratchet.ParseFindingKind(x.Kind)
				if err != nil {
					t.Fatal(err)
				}
				fs = append(fs, ratchet.Finding{Kind: kind, Rule: rules.RuleID(x.Rule), File: x.File, Line: x.Line,
					Text: x.Text, What: x.What, Why: x.Why, Where: x.Where, Fix: x.Fix})
			}
			var out bytes.Buffer
			ratchet.WriteReport(&out, format, ratchet.Command(c.Command), fs)
			if out.String() != c.Want {
				t.Errorf("output:\n%s\nwant:\n%s", out.String(), c.Want)
			}
		})
	}
	for _, want := range requiredReportCases {
		if !slices.Contains(names, want) {
			t.Errorf("testdata/report.yaml is missing required case %q", want)
		}
	}
}

type reportFixture struct {
	Cases          []reportCase `yaml:"cases"`
	InvalidFormats []string     `yaml:"invalidFormats"`
}

func loadReportFixture(t *testing.T) reportFixture {
	t.Helper()
	var f reportFixture
	dec := yaml.NewDecoder(bytes.NewReader(reportYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/report.yaml: %v", err)
	}
	return f
}

func TestParseFormatRefusesUnknown(t *testing.T) {
	for _, s := range loadReportFixture(t).InvalidFormats {
		if _, err := ratchet.ParseFormat(s); err == nil {
			t.Errorf("ParseFormat(%q) succeeded", s)
		}
	}
}
