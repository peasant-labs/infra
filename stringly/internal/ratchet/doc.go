// Package ratchet compares hits with the committed baseline and writes the
// report. It reads and writes the baseline file (counts per rule and file),
// applies stringly-allow markers, and renders every finding with the message
// contract: what, why, where and fix.
//
// It takes hits, markers, baseline text, the set of existing files, git
// renames and the tool and ast-grep versions as plain inputs, so it is tested
// without the ast-grep binary. `check` never uses git; `update` does, through
// the renames its caller supplies.
package ratchet
