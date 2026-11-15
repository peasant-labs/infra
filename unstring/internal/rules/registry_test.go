package rules_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/unstring/internal/rules"
)

type registryFixture struct {
	Valid []struct {
		Name   string        `yaml:"name"`
		Params fixtureParams `yaml:"params"`
	} `yaml:"valid"`
	Equivalent []struct {
		Name string        `yaml:"name"`
		A    fixtureParams `yaml:"a"`
		B    fixtureParams `yaml:"b"`
	} `yaml:"equivalent"`
	Invalid []struct {
		Name   string        `yaml:"name"`
		Params fixtureParams `yaml:"params"`
		Names  string        `yaml:"names"`
	} `yaml:"invalid"`
	DocumentIDs []struct {
		ID   string `yaml:"id"`
		Rule string `yaml:"rule"`
	} `yaml:"documentIds"`
	ForeignDocumentIDs []string `yaml:"foreignDocumentIds"`
}

func loadRegistryFixture(t *testing.T) registryFixture {
	t.Helper()
	var f registryFixture
	decodeStrict(t, "registry.yaml", &f)
	return f
}

// params builds Params from the fixture alone, so an empty list in the
// fixture reaches the registry as empty.
func (o fixtureParams) params() rules.Params {
	return rules.Params{
		EndpointPrefixes: o.EndpointPrefixes,
		EndpointCallees:  o.EndpointCallees,
		MountFiles:       o.MountFiles,
		DataDirs:         o.DataDirs,
	}
}

func TestRegistryHoldsExactlyTheStaticRules(t *testing.T) {
	for _, c := range loadRegistryFixture(t).Valid {
		t.Run(c.Name, func(t *testing.T) {
			reg, err := rules.NewRegistry(c.Params.params())
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			var want []rules.RuleID
			for _, id := range rules.AllRuleIDs {
				if id.Origin() == rules.OriginStatic {
					want = append(want, id)
				}
			}
			got := reg.Rules()
			if len(got) != len(want) {
				t.Fatalf("Rules() has %d rules, want the %d static ids in AllRuleIDs order", len(got), len(want))
			}
			for i, r := range got {
				if r.ID != want[i] {
					t.Errorf("Rules()[%d] = %s, want %s (AllRuleIDs order)", i, r.ID, want[i])
				}
			}
			for _, id := range rules.AllRuleIDs {
				r, ok := reg.Rule(id)
				if ok != (id.Origin() == rules.OriginStatic) {
					t.Errorf("Rule(%s) found = %v; a registry holds exactly the static rules", id, ok)
					continue
				}
				if !ok {
					continue
				}
				if r.ID != id {
					t.Errorf("Rule(%s) returned %s", id, r.ID)
				}
				if r.Message.What == "" || r.Message.Why == "" || r.Message.Fix == "" {
					t.Errorf("%s has an empty message part: %+v", id, r.Message)
				}
				if !bytes.HasPrefix(stripComments(r.Document), []byte("id: "+id.String()+"\n")) {
					t.Errorf("%s: the document does not start with its id line:\n%s", id, r.Document)
				}
			}
			if _, ok := reg.Rule("no-such-rule"); ok {
				t.Error(`Rule("no-such-rule") found a rule`)
			}
		})
	}
}

func TestRegistryRendersEquivalentParamsIdentically(t *testing.T) {
	for _, c := range loadRegistryFixture(t).Equivalent {
		t.Run(c.Name, func(t *testing.T) {
			a, err := rules.NewRegistry(c.A.params())
			if err != nil {
				t.Fatal(err)
			}
			b, err := rules.NewRegistry(c.B.params())
			if err != nil {
				t.Fatal(err)
			}
			for i, ra := range a.Rules() {
				if rb := b.Rules()[i]; !bytes.Equal(ra.Document, rb.Document) {
					t.Errorf("%s renders differently:\n%s\n---\n%s", ra.ID, ra.Document, rb.Document)
				}
			}
		})
	}
}

func TestRegistryRefusesUnsafeParams(t *testing.T) {
	for _, c := range loadRegistryFixture(t).Invalid {
		t.Run(c.Name, func(t *testing.T) {
			reg, err := rules.NewRegistry(c.Params.params())
			if err == nil {
				t.Fatalf("NewRegistry accepted %+v and built %d rules; want an error naming %s", c.Params, len(reg.Rules()), c.Names)
			}
			msg := err.Error()
			for _, part := range []string{c.Names, ".unstring.yaml", "nothing was scanned"} {
				if !strings.Contains(msg, part) {
					t.Errorf("the error does not mention %q:\n%s", part, msg)
				}
			}
		})
	}
}

func TestParseDocumentID(t *testing.T) {
	f := loadRegistryFixture(t)
	for _, c := range f.DocumentIDs {
		got, err := rules.ParseDocumentID(c.ID)
		if err != nil || got.String() != c.Rule {
			t.Errorf("ParseDocumentID(%q) = %q, %v; want %q", c.ID, got, err, c.Rule)
		}
	}
	for _, s := range f.ForeignDocumentIDs {
		if got, err := rules.ParseDocumentID(s); err == nil {
			t.Errorf("ParseDocumentID(%q) = %q; want an error, it is not one of the tool's documents", s, got)
		}
	}
}

func TestLangASTGrepSettings(t *testing.T) {
	for _, l := range rules.AllLangs {
		if l.AstGrepLanguage() == "" || len(l.FileGlobs()) == 0 {
			t.Errorf("%s has no ast-grep language or file globs", l)
		}
	}
	if rules.Lang("rust").AstGrepLanguage() != "" || rules.Lang("rust").FileGlobs() != nil {
		t.Error("an invalid Lang reports ast-grep settings")
	}
}

// stripComments drops the leading comment lines of a rule document.
func stripComments(doc []byte) []byte {
	for bytes.HasPrefix(doc, []byte("#")) {
		i := bytes.IndexByte(doc, '\n')
		if i < 0 {
			return nil
		}
		doc = doc[i+1:]
	}
	return doc
}
