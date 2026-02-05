package store

func Newer(a, b Record) Record {
	if a.Ts > b.Ts {
		return a
	}
	if b.Ts > a.Ts {
		return b
	}

	if a.WriterID >= b.WriterID {
		return a
	}
	return b
}
