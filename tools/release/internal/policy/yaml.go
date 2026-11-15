package policy

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// needsList reads a job's 'needs:' value in either form GitHub accepts, a
// scalar or a sequence of job names. An absent key yields no names.
func needsList(job *yaml.Node) ([]string, error) {
	needs := mappingValue(job, "needs")
	if needs == nil {
		return nil, nil
	}
	switch needs.Kind {
	case yaml.ScalarNode:
		if needs.Value == "" {
			return nil, fmt.Errorf("empty scalar")
		}
		return []string{needs.Value}, nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(needs.Content))
		for _, node := range needs.Content {
			if node.Kind != yaml.ScalarNode || node.Value == "" {
				return nil, fmt.Errorf("sequence contains a non-scalar or empty item")
			}
			values = append(values, node.Value)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("expected scalar or sequence, got YAML kind %d", needs.Kind)
	}
}

// mappingValue returns the value node for key in a YAML mapping, or nil when
// node is not a mapping or carries no such key.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
