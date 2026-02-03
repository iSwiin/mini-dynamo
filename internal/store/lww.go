package store

// Newer returns the record that wins under Last-Write-Wins.
// Tie-breaker: WriterID (stable).
func Newer(a, b Record) Record {
	if a.Ts > b.Ts {
		return a
	}
	if b.Ts > a.Ts {
		return b
	}
	// tie
	if a.WriterID >= b.WriterID {
		return a
	}
	return b
}
