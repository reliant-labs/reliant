package disjoint

// Bug #20 regression: a single impl struct that satisfies multiple disjoint
// (non-overlapping) contract interfaces should be accepted. The previous
// "in ALL interfaces" rule incorrectly flagged Read() as missing from Writer
// and Write() as missing from Reader.

type Reader interface {
	Read() string
}

type Writer interface {
	Write(s string) error
}
