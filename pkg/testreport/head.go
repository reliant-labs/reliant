package testreport

import (
	"errors"
	"fmt"
	"io"
)

// The document schemas this package defines. A field's meaning under a
// schema never changes; a breaking change is a new version.
const (
	SchemaResults  = "forge.test.results/v1"
	SchemaCoverage = "forge.test.coverage/v1"
	SchemaReport   = "forge.test.report/v1"
	SchemaFlakes   = "forge.test.flakes/v1"
	SchemaDiff     = "forge.test.diff/v1"
)

// Head is the head every forge --json document starts with: the schema name,
// whether the command succeeded, the exit code it exits with, and the error
// message on failure. It is field-for-field the head of forge's other
// documents, so a consumer reads every forge document's verdict the same way.
type Head struct {
	// Schema names the document and its version, e.g. "forge.test.report/v1".
	Schema string `json:"schema"`
	// OK is true exactly when ExitCode is 0.
	OK bool `json:"ok"`
	// ExitCode is the status the producing command exits with, or for a
	// stored document, the status of what it records.
	ExitCode int `json:"exit_code"`
	// Error is the failure message; absent on success.
	Error string `json:"error,omitempty"`
}

// ErrNoInput is returned by the parsers when the reader held nothing at all.
// A zero-byte input is the absence of facts, not an empty-but-valid run.
var ErrNoInput = errors.New("testreport: no input")

// Limits bounds what a parser reads and keeps, so input from a project the
// reader does not control cannot make it allocate without bound. A zero field
// takes the default named on it; a negative field means no limit (for input
// the caller already trusts).
type Limits struct {
	// MaxInputBytes bounds the bytes read. Default 512 MiB.
	MaxInputBytes int64
	// MaxLineBytes bounds one line of a line-oriented format (`go test
	// -json`, a Go profile, LCOV). A longer line is counted as malformed and
	// skipped, never buffered whole. Default 1 MiB.
	MaxLineBytes int
	// MaxCases bounds the test cases (results) or files (coverage) kept.
	// Default 1,000,000.
	MaxCases int
	// MaxPackages bounds the packages of a results input. Default 100,000.
	MaxPackages int
	// MaxBlocks bounds the distinct coverage blocks kept. Default 10,000,000.
	MaxBlocks int
	// MaxOutputBytes bounds the failure output kept per case. Default 8 KiB,
	// kept as the first and last halves with a marker between.
	MaxOutputBytes int
	// MaxTotalOutputBytes bounds the failure output kept across all cases.
	// Past it, failures keep their status and signature but no output.
	// Default 64 MiB.
	MaxTotalOutputBytes int64
}

// DefaultLimits is the limits a zero Limits takes.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:       512 << 20,
		MaxLineBytes:        1 << 20,
		MaxCases:            1_000_000,
		MaxPackages:         100_000,
		MaxBlocks:           10_000_000,
		MaxOutputBytes:      8 << 10,
		MaxTotalOutputBytes: 64 << 20,
	}
}

// resolved fills zero fields with the defaults and turns negatives into "no
// limit" (the largest value of the field's type).
func (l Limits) resolved() Limits {
	d := DefaultLimits()
	const maxInt = int(^uint(0) >> 1)
	const maxInt64 = int64(^uint64(0) >> 1)
	pickInt := func(v, def int) int {
		switch {
		case v == 0:
			return def
		case v < 0:
			return maxInt
		}
		return v
	}
	pick64 := func(v, def int64) int64 {
		switch {
		case v == 0:
			return def
		case v < 0:
			return maxInt64
		}
		return v
	}
	return Limits{
		MaxInputBytes:       pick64(l.MaxInputBytes, d.MaxInputBytes),
		MaxLineBytes:        pickInt(l.MaxLineBytes, d.MaxLineBytes),
		MaxCases:            pickInt(l.MaxCases, d.MaxCases),
		MaxPackages:         pickInt(l.MaxPackages, d.MaxPackages),
		MaxBlocks:           pickInt(l.MaxBlocks, d.MaxBlocks),
		MaxOutputBytes:      pickInt(l.MaxOutputBytes, d.MaxOutputBytes),
		MaxTotalOutputBytes: pick64(l.MaxTotalOutputBytes, d.MaxTotalOutputBytes),
	}
}

// LimitError is returned when an input exceeds one of [Limits]. The input is
// refused rather than half-read: a partial record would read as a complete
// one.
type LimitError struct {
	// Limit names the Limits field, e.g. "MaxCases".
	Limit string
	// Max is the limit's value.
	Max int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("testreport: input exceeds %s (%d)", e.Limit, e.Max)
}

// limitedReader reads at most max bytes and then fails with a *LimitError
// instead of reporting a clean EOF, so truncated input is never parsed as
// whole.
type limitedReader struct {
	r    io.Reader
	left int64
	max  int64
}

func newLimitedReader(r io.Reader, max int64) *limitedReader {
	return &limitedReader{r: r, left: max, max: max}
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// Probe for one more byte: input of exactly max bytes is fine.
		var one [1]byte
		n, err := l.r.Read(one[:])
		if n > 0 {
			return 0, &LimitError{Limit: "MaxInputBytes", Max: l.max}
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}
