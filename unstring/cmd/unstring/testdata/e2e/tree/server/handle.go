package server

func name(m map[string]any) string {
	n, _ := m["name"].(string)
	k, _ := m["kind"].(string)
	return n + k
}

func ready(s string) bool {
	switch s {
	case "ready":
		return true
	}
	return s == "public"
}
