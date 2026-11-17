package intent

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReportSchemaVersion identifies the schema-gen report wire format.
const ReportSchemaVersion = 1

type SurfaceChange string

const (
	SurfaceChangeNone       SurfaceChange = "none"
	SurfaceChangeDocs       SurfaceChange = "docs"
	SurfaceChangeFunctional SurfaceChange = "functional"
)

var AllSurfaceChanges = []SurfaceChange{SurfaceChangeNone, SurfaceChangeDocs, SurfaceChangeFunctional}

func (c SurfaceChange) IsValid() bool  { return slices.Contains(AllSurfaceChanges, c) }
func (c SurfaceChange) String() string { return string(c) }

type SurfaceReport struct {
	Name        string        `yaml:"name"`
	Breaking    bool          `yaml:"breaking"`
	Change      SurfaceChange `yaml:"change"`
	AheadOnBase bool          `yaml:"ahead_on_base"`
}

type ContractReport struct {
	Report        int             `yaml:"report"`
	Base          string          `yaml:"base"`
	Head          string          `yaml:"head"`
	GoAPIBreaking bool            `yaml:"go_api_breaking"`
	Surfaces      []SurfaceReport `yaml:"surfaces"`
}

// strictDocument rejects unknown fields, duplicate keys and multiple documents.
func strictDocument(data []byte, dst any) error {
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("multiple YAML documents")
	}
	return nil
}

func documentError(kind string, err error) error {
	return inputError("invalid "+kind+": "+err.Error(),
		"the handoff is a single strict, complete and coherent YAML document",
		"intent.Parse"+kind, "reading the release handoff",
		"no release decision can be made", "regenerate the handoff with the producer and retry")
}

// ParseContractReport requires every field, including false boolean values.
func ParseContractReport(data []byte, cfg Config) (ContractReport, error) {
	var raw struct {
		Report        *int    `yaml:"report"`
		Base          *string `yaml:"base"`
		Head          *string `yaml:"head"`
		GoAPIBreaking *bool   `yaml:"go_api_breaking"`
		Surfaces      *[]struct {
			Name        *string        `yaml:"name"`
			Breaking    *bool          `yaml:"breaking"`
			Change      *SurfaceChange `yaml:"change"`
			AheadOnBase *bool          `yaml:"ahead_on_base"`
		} `yaml:"surfaces"`
	}
	if err := strictDocument(data, &raw); err != nil {
		return ContractReport{}, documentError("ContractReport", err)
	}
	if raw.Report == nil || *raw.Report != ReportSchemaVersion || raw.Base == nil || strings.TrimSpace(*raw.Base) == "" ||
		raw.Head == nil || strings.TrimSpace(*raw.Head) == "" || raw.GoAPIBreaking == nil || raw.Surfaces == nil {
		return ContractReport{}, documentError("ContractReport", fmt.Errorf("missing required field or unsupported report version"))
	}
	rep := ContractReport{Report: *raw.Report, Base: *raw.Base, Head: *raw.Head, GoAPIBreaking: *raw.GoAPIBreaking}
	seen := map[string]bool{}
	for _, s := range *raw.Surfaces {
		if s.Name == nil || s.Breaking == nil || s.Change == nil || s.AheadOnBase == nil {
			return ContractReport{}, documentError("ContractReport", fmt.Errorf("surface has missing fields"))
		}
		if _, ok := cfg.Surfaces[*s.Name]; !ok || seen[*s.Name] {
			return ContractReport{}, documentError("ContractReport", fmt.Errorf("unknown or duplicate surface %q", *s.Name))
		}
		if !s.Change.IsValid() || (*s.Breaking && *s.Change != SurfaceChangeFunctional) {
			return ContractReport{}, documentError("ContractReport", fmt.Errorf("surface %q has an invalid or incoherent change", *s.Name))
		}
		seen[*s.Name] = true
		rep.Surfaces = append(rep.Surfaces, SurfaceReport{*s.Name, *s.Breaking, *s.Change, *s.AheadOnBase})
	}
	if len(seen) != len(cfg.Surfaces) {
		return ContractReport{}, documentError("ContractReport", fmt.Errorf("surface names do not match the release config"))
	}
	return rep, nil
}

func contractRejection(code ErrorCode, what, why, fix string) error {
	return &Error{Code: code, What: what, Why: why, Where: "contract report, judged by release-intent",
		When: "checking release intent", Means: "the contract check cannot pass", Fix: fix}
}

func breakingReport(rep ContractReport) bool {
	if rep.GoAPIBreaking {
		return true
	}
	for _, s := range rep.Surfaces {
		if s.Breaking {
			return true
		}
	}
	return false
}

// CheckContract judges a PR title after its syntax has been validated.
func CheckContract(cfg Config, current SemVer, rep ContractReport, raw string) (Title, error) {
	title, err := ParseTitle(cfg, current, raw)
	if err != nil {
		return Title{}, err
	}
	if breakingReport(rep) && !title.Breaking {
		fix := "add ! with the required level: for example release![minor]: declare the break below 1.0, or release![major]: declare the break from 1.0"
		return Title{}, contractRejection(ErrUnderDeclaredBreaking, "the report identifies an undeclared breaking change",
			"a Go API or surface break requires a breaking mark",
			fix)
	}
	if !cfg.SpecFreeze {
		return title, nil
	}
	var missing, extra []string
	for _, s := range rep.Surfaces {
		listed := slices.Contains(title.Scopes, s.Name)
		if s.Change == SurfaceChangeFunctional && !listed {
			missing = append(missing, s.Name)
		}
		if listed && s.Change == SurfaceChangeNone && !s.AheadOnBase {
			extra = append(extra, s.Name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if title.Level == LevelNone {
		for _, s := range rep.Surfaces {
			if s.Change == SurfaceChangeFunctional {
				return Title{}, contractRejection(ErrSpecChangeNeedsBump, "surface "+strconv.Quote(s.Name)+" changed functionally without a release",
					"a functional spec change must be included in a release",
					"retitle with a releasing type and level, for example fix("+s.Name+")[patch]: describe the change")
			}
		}
		return title, nil
	}
	if len(missing) > 0 || len(extra) > 0 {
		return Title{}, contractRejection(ErrSurfaceMismatch, "surface scopes do not match the report: add "+fmt.Sprint(missing)+"; drop "+fmt.Sprint(extra),
			"functional surfaces must be declared and declared surfaces must have changed or be ahead on the base",
			"adjust the named title scopes to match the report")
	}
	return title, nil
}

// CheckContractRange can judge a break across the range but cannot attribute a
// particular surface to a commit; freeze planning covers that attribution.
func CheckContractRange(_ Config, rep ContractReport, subjects []Title) error {
	if breakingReport(rep) {
		for _, title := range subjects {
			if title.Breaking {
				return nil
			}
		}
		return contractRejection(ErrUnderDeclaredBreaking, "the report identifies a break with no breaking title in the range",
			"a Go API or surface break requires a breaking mark",
			"land release![minor]: declare the break below 1.0, or release![major]: declare the break from 1.0")
	}
	return nil
}
