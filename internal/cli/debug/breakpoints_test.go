package debug

import "testing"

// TestIsFileLineSpec pins the routing decision behind bug #1: a positional
// `break` argument is a file:line spec only when the part after the LAST colon
// is an integer line number. Everything else is a function spec that Delve's
// location parser resolves (main.Foo, runtime.gopark, (*T).Method, a bare
// name). Before the fix, the wrapper forced every positional arg through
// parseFileLine and rejected function names outright.
func TestIsFileLineSpec(t *testing.T) {
	cases := []struct {
		spec string
		want bool
	}{
		// file:line — line breakpoints.
		{"handler.go:42", true},
		{"internal/foo/bar.go:1", true},
		{`C:\proj\handler.go:42`, true}, // last-colon rule handles Windows paths
		{"a:b:99", true},                // last segment is the line number

		// function specs — NOT file:line.
		{"main.compute", false},
		{"runtime.gopark", false},
		{"main.main", false},
		{"(*Server).Serve", false},
		{"github.com/x/y/pkg.Func", false}, // last colon... there is none
		{"handleRequest", false},           // bare short name
		{"handler.go:", false},             // trailing colon, no number
		{"handler.go:abc", false},          // non-numeric after colon
		{"", false},
	}
	for _, tc := range cases {
		if got := isFileLineSpec(tc.spec); got != tc.want {
			t.Errorf("isFileLineSpec(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}
