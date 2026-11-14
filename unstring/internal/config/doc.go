// Package config reads and validates a repository's `.unstring.yaml`: the
// roots to scan per language, the baseline path, the schema module, the
// endpoint and data-directory settings, and the `exempt` entries.
//
// Unknown keys, an unknown rule id in `exempt`, an `exempt` entry with no
// reason, and an `exempt` entry for a closed-set rule that matches a
// hand-written file all stop the run.
package config
