package store

// Newer implements Last-Write-Wins.
// - Higher Ts wins.
// - If Ts ties, higher WriterID (lexicographically) wins for determinism.
// Tombstones are just records with Deleted=true and a newer timestamp.
func Newer(a, b Record) Record {
	if a.Ts > b.Ts {
		return a
	}
	if b.Ts > a.Ts {
		return b
	}
	// tie-break deterministically
	if a.WriterID >= b.WriterID {
		return a
	}
	return b
}
