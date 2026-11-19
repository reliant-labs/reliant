// Package fakes is test support: a canned runner that only test files import.
// Its own files are not _test.go files, because tests in more than one package
// share it, but every read in them runs under a test. A field the fake reads
// and only tests write is the test's input, not a shape production cannot
// produce, so none of it may be judged.
package fakes

// Row is one canned answer.
type Row struct {
	// OK: read by the fake, written by the test that configures it.
	Value string
}

// Runner answers from Rows.
type Runner struct {
	// OK: same.
	Rows []Row
}

// First is the fake's read of its configuration.
func (r *Runner) First() string {
	if len(r.Rows) == 0 {
		return ""
	}
	return r.Rows[0].Value
}

// Echo is a no-op with parameters, which noop-func does not judge in test code.
func Echo(s string) error { return nil }
