// Package astgrep runs the ast-grep binary: it asserts the one version the
// tool supports, writes rule documents and an sgconfig.yml to a temporary
// directory, runs `ast-grep scan --json=stream`, and decodes the matches.
//
// It knows nothing about which rules are the tool's own. The checker turns
// matches of tool rules into hits; the catalog reads matches of its discovery
// rules. Records of any other rule (for example ast-grep's own
// `unused-suppression`) are left to the caller to ignore.
package astgrep
