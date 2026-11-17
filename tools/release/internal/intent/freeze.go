package intent

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const SurfaceStateSchemaVersion = 1
const FreezePlanSchemaVersion = 1

type SurfaceStateKind string

const (
	SurfaceClean     SurfaceStateKind = "clean"
	SurfaceDocsAhead SurfaceStateKind = "docs-ahead"
	SurfaceAhead     SurfaceStateKind = "ahead"
)

var AllSurfaceStates = []SurfaceStateKind{SurfaceClean, SurfaceDocsAhead, SurfaceAhead}

func (s SurfaceStateKind) IsValid() bool { return slices.Contains(AllSurfaceStates, s) }

type SurfaceState struct {
	Name   string           `yaml:"name"`
	Label  string           `yaml:"label"`
	Frozen SemVer           `yaml:"frozen"`
	State  SurfaceStateKind `yaml:"state"`
}

type SurfaceStates struct {
	Status   int            `yaml:"status"`
	Surfaces []SurfaceState `yaml:"surfaces"`
}

// FreezeLevel serializes a Level as the word understood by the handoff, not
// as the integer used by the title arithmetic.
type FreezeLevel Level

func (l FreezeLevel) String() string { return Level(l).String() }

func (l FreezeLevel) MarshalYAML() (any, error) {
	if !Level(l).IsValid() {
		return nil, fmt.Errorf("invalid freeze level %q", l.String())
	}
	return l.String(), nil
}

func (l *FreezeLevel) UnmarshalYAML(node *yaml.Node) error {
	var word string
	if err := node.Decode(&word); err != nil {
		return err
	}
	level, err := ParseLevel(word)
	if err != nil {
		return err
	}
	*l = FreezeLevel(level)
	return nil
}

// FreezeSurface is a total row: unchanged surfaces have level none, equal from
// and to versions, and freeze false.
type FreezeSurface struct {
	Name     string      `yaml:"name"`
	Label    string      `yaml:"label"`
	From     SemVer      `yaml:"from"`
	To       SemVer      `yaml:"to"`
	Level    FreezeLevel `yaml:"level"`
	Breaking bool        `yaml:"breaking"`
	Freeze   bool        `yaml:"freeze"`
}

type FreezePlan struct {
	Plan     int             `yaml:"plan"`
	Surfaces []FreezeSurface `yaml:"surfaces"`
}

func safeLabel(s string) bool {
	return strings.TrimSpace(s) != "" && strings.IndexFunc(s, unicode.IsControl) < 0
}

// ParseSurfaceStates decodes all required fields and checks exact membership.
func ParseSurfaceStates(data []byte, cfg Config) (SurfaceStates, error) {
	var raw struct {
		Status   *int `yaml:"status"`
		Surfaces *[]struct {
			Name   *string           `yaml:"name"`
			Label  *string           `yaml:"label"`
			Frozen *SemVer           `yaml:"frozen"`
			State  *SurfaceStateKind `yaml:"state"`
		} `yaml:"surfaces"`
	}
	if err := strictDocument(data, &raw); err != nil {
		return SurfaceStates{}, documentError("SurfaceStates", err)
	}
	if raw.Status == nil || *raw.Status != SurfaceStateSchemaVersion || raw.Surfaces == nil || len(cfg.Surfaces) == 0 {
		return SurfaceStates{}, documentError("SurfaceStates", fmt.Errorf("missing fields, unsupported status version, or no configured surfaces"))
	}
	out := SurfaceStates{Status: *raw.Status}
	seen := map[string]bool{}
	for _, s := range *raw.Surfaces {
		if s.Name == nil || s.Label == nil || s.Frozen == nil || s.State == nil ||
			!safeLabel(*s.Label) || *s.Frozen == "" || !s.State.IsValid() {
			return SurfaceStates{}, documentError("SurfaceStates", fmt.Errorf("a surface has missing or invalid fields"))
		}
		if _, ok := cfg.Surfaces[*s.Name]; !ok || seen[*s.Name] {
			return SurfaceStates{}, stateSetError("unknown or duplicate state surface "+strconvQuote(*s.Name), "intent.ParseSurfaceStates")
		}
		if _, err := ParseSemVer(s.Frozen.String()); err != nil {
			return SurfaceStates{}, documentError("SurfaceStates", err)
		}
		seen[*s.Name] = true
		out.Surfaces = append(out.Surfaces, SurfaceState{*s.Name, *s.Label, *s.Frozen, *s.State})
	}
	if len(seen) != len(cfg.Surfaces) {
		return SurfaceStates{}, stateSetError("state surface names do not match the release config", "intent.ParseSurfaceStates")
	}
	return out, nil
}

func stateSetError(what, where string) error {
	return &Error{Code: ErrConfigInvalid, What: what,
		Why:   "the state must name every configured surface exactly once",
		Where: where, When: "reading the surface state",
		Means: "no freeze plan can be computed",
		Fix:   "regenerate the state using the current release config"}
}

// PlanFreeze computes one version per ahead surface from releasing titles.
// Titles for clean and docs-only surfaces do not trigger a freeze.
func PlanFreeze(cfg Config, states SurfaceStates, subjects []Title) (FreezePlan, error) {
	if states.Status != SurfaceStateSchemaVersion || len(cfg.Surfaces) == 0 || len(states.Surfaces) != len(cfg.Surfaces) {
		return FreezePlan{}, stateSetError("surface states do not match the release config", "intent.PlanFreeze")
	}
	statesByName := map[string]SurfaceState{}
	seen := map[string]bool{}
	plan := FreezePlan{Plan: FreezePlanSchemaVersion, Surfaces: []FreezeSurface{}}
	for _, state := range states.Surfaces {
		if _, ok := cfg.Surfaces[state.Name]; !ok || seen[state.Name] || !state.State.IsValid() || !safeLabel(state.Label) {
			return FreezePlan{}, stateSetError("invalid or duplicate surface "+strconvQuote(state.Name), "intent.PlanFreeze")
		}
		seen[state.Name] = true
		statesByName[state.Name] = state
	}
	for _, name := range cfg.SurfaceNames() {
		state := statesByName[name]
		if _, err := ParseSemVer(state.Frozen.String()); err != nil {
			return FreezePlan{}, documentError("SurfaceStates", err)
		}
		row := FreezeSurface{Name: state.Name, Label: state.Label, From: state.Frozen, To: state.Frozen, Level: FreezeLevel(LevelNone)}
		if state.State == SurfaceAhead {
			var level Level
			for _, title := range subjects {
				if slices.Contains(title.Scopes, state.Name) && title.Level != LevelNone {
					if title.Level > level {
						level = title.Level
					}
					row.Breaking = row.Breaking || title.Breaking
				}
			}
			if level == LevelNone {
				return FreezePlan{}, &Error{Code: ErrSurfaceUndeclared,
					What:  "ahead surface " + strconvQuote(state.Name) + " has no releasing declaration in the range",
					Why:   "an ahead surface cannot be frozen without a title naming it and its release level",
					Where: "surface state and landed titles, judged by intent.PlanFreeze",
					When:  "planning the spec freeze", Means: "the surface cannot be frozen",
					Fix: "land release(" + state.Name + ")[minor]: declare the pending " + state.Label + " change"}
			}
			next, err := NextVersion(state.Frozen, level, false)
			if err != nil {
				return FreezePlan{}, err
			}
			row.To, row.Level, row.Freeze = next, FreezeLevel(level), true
		}
		plan.Surfaces = append(plan.Surfaces, row)
	}
	return plan, nil
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// ParseFreezePlan rejects incomplete or incoherent handoffs before they can
// influence a release commit title.
func ParseFreezePlan(data []byte, cfg Config) (FreezePlan, error) {
	var raw struct {
		Plan     *int `yaml:"plan"`
		Surfaces *[]struct {
			Name     *string      `yaml:"name"`
			Label    *string      `yaml:"label"`
			From     *SemVer      `yaml:"from"`
			To       *SemVer      `yaml:"to"`
			Level    *FreezeLevel `yaml:"level"`
			Breaking *bool        `yaml:"breaking"`
			Freeze   *bool        `yaml:"freeze"`
		} `yaml:"surfaces"`
	}
	if err := strictDocument(data, &raw); err != nil {
		return FreezePlan{}, documentError("FreezePlan", err)
	}
	if raw.Plan == nil || *raw.Plan != FreezePlanSchemaVersion || raw.Surfaces == nil || len(cfg.Surfaces) == 0 {
		return FreezePlan{}, documentError("FreezePlan", fmt.Errorf("missing fields, unsupported plan version, or no configured surfaces"))
	}
	out := FreezePlan{Plan: *raw.Plan, Surfaces: []FreezeSurface{}}
	seen := map[string]bool{}
	for _, s := range *raw.Surfaces {
		if s.Name == nil || s.Label == nil || s.From == nil || s.To == nil || s.Level == nil || s.Breaking == nil || s.Freeze == nil || !safeLabel(*s.Label) || *s.From == "" || *s.To == "" {
			return FreezePlan{}, documentError("FreezePlan", fmt.Errorf("a surface has missing or invalid fields"))
		}
		if _, ok := cfg.Surfaces[*s.Name]; !ok || seen[*s.Name] {
			return FreezePlan{}, documentError("FreezePlan", fmt.Errorf("unknown or duplicate surface %q", *s.Name))
		}
		level := Level(*s.Level)
		next, err := NextVersion(*s.From, level, false)
		if err != nil || next != *s.To || (*s.Freeze && level == LevelNone) || (!*s.Freeze && (level != LevelNone || *s.Breaking)) {
			return FreezePlan{}, documentError("FreezePlan", fmt.Errorf("surface %q has an incoherent version, level or freeze flag", *s.Name))
		}
		seen[*s.Name] = true
		out.Surfaces = append(out.Surfaces, FreezeSurface{*s.Name, *s.Label, *s.From, *s.To, *s.Level, *s.Breaking, *s.Freeze})
	}
	if len(seen) != len(cfg.Surfaces) {
		return FreezePlan{}, documentError("FreezePlan", fmt.Errorf("surface names do not match the release config"))
	}
	byName := make(map[string]FreezeSurface, len(out.Surfaces))
	for _, surface := range out.Surfaces {
		byName[surface.Name] = surface
	}
	out.Surfaces = out.Surfaces[:0]
	for _, name := range cfg.SurfaceNames() {
		out.Surfaces = append(out.Surfaces, byName[name])
	}
	return out, nil
}
