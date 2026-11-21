// Package sqlite stands for a vendored copy of an upstream driver: its
// literals belong to the upstream's vocabulary, not to the repository's sets.
package sqlite

type journalMode string

const (
	journalWAL    journalMode = "wal"
	journalDelete journalMode = "delete"
)

func isMain(db string) bool {
	switch db {
	case "", "ready":
		return true
	}
	return db == "public"
}
