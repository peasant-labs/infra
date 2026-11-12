package intent_test

// Title grammar suite: drives intent.ParseTitle over the title corpus.

import (
	_ "embed"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/title_corpus.yaml
var titleCorpusYAML []byte

type titleInput struct {
	Title string `yaml:"title"`
	// TitleHex carries a title as hex bytes, for input that is not valid
	// UTF-8 and so cannot be written as YAML text.
	TitleHex string `yaml:"title_hex"`
	Current  string `yaml:"current"`
	Releases bool   `yaml:"releases"`
}

type titleExpected struct {
	Type     intent.Type      `yaml:"type"`
	Scopes   []string         `yaml:"scopes"`
	Breaking bool             `yaml:"breaking"`
	Level    intent.Level     `yaml:"level"`
	Subject  string           `yaml:"subject"`
	PR       int              `yaml:"pr"`
	Code     intent.ErrorCode `yaml:"code"`
}

func TestParseTitle(t *testing.T) {
	corpus := loadNamedCorpus[titleInput, titleExpected](t, titleCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			cfg := intent.Config{Releases: c.Input.Releases}
			raw := c.Input.Title
			if c.Input.TitleHex != "" {
				decoded, hexErr := hex.DecodeString(c.Input.TitleHex)
				if hexErr != nil {
					t.Fatalf("decode title_hex: %v", hexErr)
				}
				raw = string(decoded)
			}
			got, err := intent.ParseTitle(cfg, intent.SemVer(c.Input.Current), raw)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("ParseTitle(%q): expected an error, got %+v", c.Input.Title, got)
				}
				requireSixParts(t, err, c.Expected.Code)
				return
			}
			if err != nil {
				t.Fatalf("ParseTitle(%q): unexpected error: %v", c.Input.Title, err)
			}
			if got.Type != c.Expected.Type {
				t.Errorf("Type = %q, want %q", got.Type, c.Expected.Type)
			}
			if !reflect.DeepEqual(append([]string(nil), got.Scopes...), append([]string(nil), c.Expected.Scopes...)) {
				t.Errorf("Scopes = %q, want %q", got.Scopes, c.Expected.Scopes)
			}
			if got.Breaking != c.Expected.Breaking {
				t.Errorf("Breaking = %v, want %v", got.Breaking, c.Expected.Breaking)
			}
			if got.Level != c.Expected.Level {
				t.Errorf("Level = %q, want %q", got.Level, c.Expected.Level)
			}
			if got.Subject != c.Expected.Subject {
				t.Errorf("Subject = %q, want %q", got.Subject, c.Expected.Subject)
			}
			if got.PR != c.Expected.PR {
				t.Errorf("PR = %d, want %d", got.PR, c.Expected.PR)
			}
			// The formatter is the parser's inverse on every accepted title.
			if text := intent.FormatTitle(got); text != strings.TrimSpace(raw) {
				t.Errorf("FormatTitle(ParseTitle(%q)) = %q", raw, text)
			}
		})
	}
}
