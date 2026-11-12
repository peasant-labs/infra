// Package rules holds the vocabulary every other stringly package shares: the
// closed set of rule ids, the languages they belong to, the operands the
// closed-set rules exclude, the Rule value written to ast-grep, and the Hit a
// scan produces.
//
// It also owns the static rules: the sixteen ast-grep rules embedded in the
// tool, their messages and their fixtures. The two closed-set rules are
// generated from the catalog on every run (package catalog) and use the same
// Rule type.
//
// This package imports no other stringly package, so config, astgrep, ratchet
// and catalog can all depend on it.
package rules
