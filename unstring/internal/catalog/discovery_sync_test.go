package catalog

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// The schema rule differs only in its last, schema-specific exclusion.
func TestSchemaUnionDiscoveryStaysInSync(t *testing.T) {
	read := func(path string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := yaml.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	common := read("discovery/union-inline.yml")
	schema := read("discovery/schema-union-inline.yml")
	for _, key := range []string{"id", "message"} {
		delete(common, key)
		delete(schema, key)
	}
	rule, ok := schema["rule"].(map[string]any)
	if !ok {
		t.Fatal("schema rule missing")
	}
	inside, ok := rule["inside"].(map[string]any)
	if !ok {
		t.Fatal("schema inside missing")
	}
	clauses, ok := inside["all"].([]any)
	if !ok || len(clauses) == 0 {
		t.Fatal("schema exclusion missing")
	}
	inside["all"] = clauses[:len(clauses)-1]
	if !reflect.DeepEqual(common, schema) {
		t.Fatal("union discovery rules drifted beyond the schema exclusion")
	}
}
