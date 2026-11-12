package rules

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// staticDocuments holds one ast-grep rule document per static rule id, named
// `<rule-id>.yml`. A document is a text/template rendered with the
// repository's Params; most use no parameter at all.
//
//go:embed static/*.yml
var staticDocuments embed.FS

// PartSeparator joins a rule id and a part name in the id of an extra
// ast-grep document, for example `go-endpoint-literal.mount-file`. ast-grep
// refuses two documents with one id, so a rule whose matcher needs a second
// `files:` scope carries it as a second document with a part id. Every match
// of a part counts as a hit of the rule; ParseDocumentID maps it back.
const PartSeparator = "."

// staticSeverity is the ast-grep severity of every rule document. The
// checker alone decides pass or fail, so no rule uses `error`.
const staticSeverity = "warning"

// NewRegistry builds the sixteen static rules for one repository: it renders
// each embedded document with p and checks the result. The returned Registry
// holds exactly the ids whose Origin is OriginStatic.
//
// It fails when a parameter cannot be written into a rule safely (see
// Params), and when an embedded document does not render to a valid rule of
// its id, language and severity; the second is a defect of the tool, never of
// the repository.
func NewRegistry(p Params) (Registry, error) {
	data, err := newTemplateData(p)
	if err != nil {
		return nil, err
	}
	if err := checkEmbeddedSet(); err != nil {
		return nil, err
	}
	reg := &registry{byID: make(map[RuleID]int)}
	for _, id := range AllRuleIDs {
		if id.Origin() != OriginStatic {
			continue
		}
		rule, err := renderRule(id, data)
		if err != nil {
			return nil, err
		}
		reg.byID[id] = len(reg.rules)
		reg.rules = append(reg.rules, rule)
	}
	return reg, nil
}

// ParseDocumentID converts the id of an ast-grep document, as reported in a
// scan's `ruleId`, into the tool's RuleID: either the rule id itself or a
// part id `<rule-id>.<part>` (see PartSeparator). It fails for a document
// that is not one of the tool's, such as ast-grep's own unused-suppression
// record.
func ParseDocumentID(s string) (RuleID, error) {
	base, part, hasPart := strings.Cut(s, PartSeparator)
	if hasPart && !partName.MatchString(part) {
		return "", fmt.Errorf("ast-grep document id %q has the part %q, which is not a lowercase part name; "+
			"the tool's documents are named <rule-id> or <rule-id>%s<part>", s, part, PartSeparator)
	}
	id, err := ParseRuleID(base)
	if err != nil {
		return "", fmt.Errorf("ast-grep document id %q is not one of the tool's documents: %w", s, err)
	}
	return id, nil
}

type registry struct {
	rules []Rule
	byID  map[RuleID]int
}

func (r *registry) Rules() []Rule { return slices.Clone(r.rules) }

func (r *registry) Rule(id RuleID) (Rule, bool) {
	i, ok := r.byID[id]
	if !ok {
		return Rule{}, false
	}
	return r.rules[i], true
}

// templateData is what a rule document may reference. Every regex fragment
// is already escaped for both the Rust regex engine and a single-quoted YAML
// scalar, so a document writes it as '...{{ .PrefixRegex }}...'.
type templateData struct {
	// PrefixRegex is a non-capturing alternation of the endpoint prefixes.
	PrefixRegex string
	// CalleeRegex is an anchored alternation of the client callees, or ""
	// when the repository names none.
	CalleeRegex string
	// DataDirRegex is a non-capturing alternation of the data directories.
	DataDirRegex string
	// MountFiles are the router files, each safe inside single quotes.
	MountFiles []string
	// PartSeparator is the separator of a part document's id.
	PartSeparator string
}

var (
	// An endpoint prefix is an absolute URL path such as /api/v1.
	prefixShape = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)
	// A callee is a JS identifier, optionally dotted (client.get).
	calleeShape = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)
	// A mount file is a relative path with `/` separators.
	mountFileShape = regexp.MustCompile(`^[A-Za-z0-9._@+-]+(/[A-Za-z0-9._@+-]+)*$`)
	// A data directory is one path segment.
	dataDirShape = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)
	// A part name is lowercase words joined by hyphens.
	partName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

func newTemplateData(p Params) (templateData, error) {
	var errs []error
	paramError := func(key, value, shape string) {
		errs = append(errs, fmt.Errorf("stringly: the %s entry %q in .stringly.yaml cannot be written into the static rules: "+
			"it must be %s. The rules are built before any scan, so nothing was scanned. "+
			"Fix the entry in .stringly.yaml, or remove the key to use the default", key, value, shape))
	}
	for _, v := range p.EndpointPrefixes {
		if !prefixShape.MatchString(v) {
			paramError("endpoints.prefixes", v, "an absolute URL path such as /api/v1: a leading /, segments of letters, digits, . _ ~ or -, and no trailing /")
		}
	}
	for _, v := range p.EndpointCallees {
		if !calleeShape.MatchString(v) {
			paramError("endpoints.callees", v, "a function name such as api or client.get")
		}
	}
	for _, v := range p.MountFiles {
		if !mountFileShape.MatchString(v) || slices.Contains(strings.Split(v, "/"), "..") || slices.Contains(strings.Split(v, "/"), ".") {
			paramError("endpoints.mountFiles", v, "a file path relative to the directory of .stringly.yaml, with / separators and no . or .. segment, such as backend/internal/router/router.go")
		}
	}
	for _, v := range p.DataDirs {
		if !dataDirShape.MatchString(v) || v == "." || v == ".." {
			paramError("dataDirs", v, "one directory name such as testdata, with no /")
		}
	}
	if len(p.EndpointPrefixes) == 0 {
		errs = append(errs, errors.New("stringly: endpoints.prefixes is empty, so the endpoint rules would have no prefix to look for. "+
			"The rules are built before any scan, so nothing was scanned. "+
			"Remove the key from .stringly.yaml to use the default (/api/v1), or list the repository's API prefix"))
	}
	if len(p.DataDirs) == 0 {
		errs = append(errs, errors.New("stringly: dataDirs is empty, so the data-path rules would have no directory to look for. "+
			"The rules are built before any scan, so nothing was scanned. "+
			"Remove the key from .stringly.yaml to use the default (testdata, fixtures, __fixtures__, captures), or list the repository's data directories"))
	}
	if len(errs) > 0 {
		return templateData{}, errors.Join(errs...)
	}
	data := templateData{
		PrefixRegex:   "(?:" + alternation(p.EndpointPrefixes) + ")",
		DataDirRegex:  "(?:" + alternation(p.DataDirs) + ")",
		MountFiles:    sortedUnique(p.MountFiles),
		PartSeparator: PartSeparator,
	}
	if len(p.EndpointCallees) > 0 {
		data.CalleeRegex = "^(?:" + alternation(p.EndpointCallees) + ")$"
	}
	return data, nil
}

// alternation quotes each value for a regex and joins them with |, in byte
// order without duplicates, so equal Params render byte-identical rules.
func alternation(values []string) string {
	vs := sortedUnique(values)
	for i, v := range vs {
		vs[i] = regexp.QuoteMeta(v)
	}
	return strings.Join(vs, "|")
}

func sortedUnique(values []string) []string {
	vs := slices.Clone(values)
	slices.Sort(vs)
	return slices.Compact(vs)
}

// checkEmbeddedSet fails when an embedded document names no static rule, so
// a document cannot be left behind after its rule is removed.
func checkEmbeddedSet() error {
	entries, err := fs.ReadDir(staticDocuments, "static")
	if err != nil {
		return fmt.Errorf("stringly: reading the embedded static rules failed: %w; this is a defect of the tool build, report it to the infra maintainers", err)
	}
	for _, e := range entries {
		id := RuleID(strings.TrimSuffix(e.Name(), ".yml"))
		if id.Origin() != OriginStatic {
			return fmt.Errorf("stringly: the embedded rule document static/%s names no static rule id; "+
				"this is a defect of the tool: remove the file or add its id to AllRuleIDs", e.Name())
		}
	}
	return nil
}

// document is the part of an ast-grep rule document the registry checks.
type document struct {
	ID       string    `yaml:"id"`
	Language string    `yaml:"language"`
	Severity string    `yaml:"severity"`
	Message  string    `yaml:"message"`
	Metadata metadata  `yaml:"metadata"`
	Files    []string  `yaml:"files"`
	Utils    yaml.Node `yaml:"utils"`
	Rule     yaml.Node `yaml:"rule"`
}

// metadata carries the rest of the message contract. ast-grep accepts and
// ignores `metadata`.
type metadata struct {
	Why string `yaml:"why"`
	Fix string `yaml:"fix"`
}

func renderRule(id RuleID, data templateData) (Rule, error) {
	name := "static/" + id.String() + ".yml"
	defect := func(format string, args ...any) error {
		return fmt.Errorf("stringly: the embedded rule document %s is invalid: %s; "+
			"this is a defect of the tool, not of the repository: report it to the infra maintainers",
			name, fmt.Sprintf(format, args...))
	}
	src, err := staticDocuments.ReadFile(name)
	if err != nil {
		return Rule{}, defect("it is missing (%v)", err)
	}
	tmpl, err := template.New(path.Base(name)).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return Rule{}, defect("it does not parse as a template (%v)", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return Rule{}, defect("it does not render (%v)", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.KnownFields(true)
	var docs []document
	for {
		var d document
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Rule{}, defect("it does not decode as rule YAML after rendering (%v)", err)
		}
		docs = append(docs, d)
	}
	if len(docs) == 0 {
		return Rule{}, defect("it holds no document")
	}
	seen := map[string]bool{}
	for i, d := range docs {
		switch {
		case i == 0 && d.ID != id.String():
			return Rule{}, defect("its first document has id %q, want %q", d.ID, id)
		case i > 0:
			got, err := ParseDocumentID(d.ID)
			if err != nil || got != id || d.ID == id.String() {
				return Rule{}, defect("document %d has id %q, want %s%s<part>", i+1, d.ID, id, PartSeparator)
			}
		}
		if seen[d.ID] {
			return Rule{}, defect("the id %q is used twice; ast-grep refuses duplicate ids", d.ID)
		}
		seen[d.ID] = true
		if d.Language != id.Lang().AstGrepLanguage() {
			return Rule{}, defect("document %q has language %q, want %q for a %s rule", d.ID, d.Language, id.Lang().AstGrepLanguage(), id.Lang())
		}
		if d.Severity != staticSeverity {
			return Rule{}, defect("document %q has severity %q, want %q", d.ID, d.Severity, staticSeverity)
		}
		if strings.TrimSpace(d.Message) == "" || d.Rule.Kind == 0 {
			return Rule{}, defect("document %q needs a message and a rule", d.ID)
		}
	}
	msg := Message{
		What: strings.TrimSpace(docs[0].Message),
		Why:  strings.TrimSpace(docs[0].Metadata.Why),
		Fix:  strings.TrimSpace(docs[0].Metadata.Fix),
	}
	if msg.Why == "" || msg.Fix == "" {
		return Rule{}, defect("its first document needs metadata.why and metadata.fix")
	}
	return Rule{ID: id, Document: out.Bytes(), Message: msg}, nil
}
