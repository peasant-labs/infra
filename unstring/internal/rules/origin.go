package rules

import "fmt"

// Origin is the closed set of places a rule comes from.
type Origin string

const (
	// OriginStatic is an ast-grep rule embedded in the tool with //go:embed.
	OriginStatic Origin = "static"
	// OriginGenerated is an ast-grep rule built in memory from the catalog on
	// every run, never written to the scanned repository.
	OriginGenerated Origin = "generated"
)

// AllOrigins lists every Origin in a fixed order.
var AllOrigins = []Origin{OriginStatic, OriginGenerated}

// String returns the wire value.
func (o Origin) String() string { return string(o) }

// IsValid reports whether o is a member of AllOrigins.
func (o Origin) IsValid() bool {
	switch o {
	case OriginStatic, OriginGenerated:
		return true
	}
	return false
}

// ParseOrigin converts a wire value into an Origin.
func ParseOrigin(s string) (Origin, error) {
	if o := Origin(s); o.IsValid() {
		return o, nil
	}
	return "", fmt.Errorf("unknown rule origin %q: a rule is one of %s", s, joinStrings(AllOrigins))
}
