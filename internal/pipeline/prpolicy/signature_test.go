package prpolicy

import (
	"strings"
	"testing"
)

func TestFailureSignatureIgnoresNoise(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"durations", "--- FAIL: TestX (0.03s)\nok pkg 1.234s", "--- FAIL: TestX (12.50s)\nok pkg 0.2s"},
		{"composite durations", "timed out after 1m2.5s", "timed out after 59.9s"},
		{"milliseconds", "took 350ms", "took 12ms"},
		{"timestamps", "2026-11-26T10:00:01.123Z error: boom", "2026-11-27T23:59:59+02:00 error: boom"},
		{"clock times", "10:00:01 boom", "23:59:59.5 boom"},
		{"temp paths", "open /tmp/TestX123/001/a.txt: no such file", "open /tmp/TestX987/002/a.txt: no such file"},
		{"macOS temp", "open /private/var/folders/ab/xyz/T/TestX1/a: no", "open /var/folders/cd/qrs/T/TestX2/a: no"},
		{"windows temp", `open C:\Users\me\AppData\Local\Temp\TestX1\a: no`, `open C:\Users\you\AppData\Local\Temp\TestX9\a: no`},
		{"worktree checkouts", "/home/a/.reliant/worktrees/abc123/internal/x.go:10: bad", "/home/b/.reliant/worktrees/zz9/internal/x.go:77: bad"},
		{"hex addresses", "panic: nil map at 0xc000123abc +0x1d", "panic: nil map at 0xc0009f0010 +0x2f"},
		{"long hex ids", "head 3f2a9b1c0d4e5f60718293a4 failed", "head aaaaaaaaaaaabbbbbbbbbbbb failed"},
		{"uuids", "chat 123e4567-e89b-12d3-a456-426614174000 gone", "chat 00000000-0000-4000-8000-000000000001 gone"},
		{"line numbers", "foo_test.go:42: want 1, got 2", "foo_test.go:57: want 1, got 2"},
		{"line and column", "x.go:12:7: undefined: y", "x.go:13:9: undefined: y"},
		{"line words", "error at line 12", "error at line 140"},
		{"goroutines", "goroutine 17 [running]:", "goroutine 4211 [running]:"},
		{"loopback ports", "dial 127.0.0.1:54321: refused", "dial 127.0.0.1:61000: refused"},
		{"pids", "process pid=4242 exited", "process pid=99 exited"},
		{"whitespace", "  want 1,\tgot 2  \r\n\n", "want 1, got 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sa := FailureSignature([]CheckFailure{{Check: "test", Test: "pkg TestX", Detail: c.a}})
			sb := FailureSignature([]CheckFailure{{Check: "test", Test: "pkg TestX", Detail: c.b}})
			if sa != sb {
				t.Errorf("noise changed the signature:\n%q -> %q\n%q -> %q", c.a, normalizeText(c.a), c.b, normalizeText(c.b))
			}
		})
	}
}

func TestFailureSignatureSeesRealChanges(t *testing.T) {
	base := CheckFailure{Check: "test", Test: "pkg TestX", Detail: "foo_test.go:42: want 1, got 2"}
	cases := []struct {
		name  string
		other CheckFailure
	}{
		{"another check", CheckFailure{Check: "lint", Test: base.Test, Detail: base.Detail}},
		{"another test", CheckFailure{Check: base.Check, Test: "pkg TestY", Detail: base.Detail}},
		{"another file", CheckFailure{Check: base.Check, Test: base.Test, Detail: "bar_test.go:42: want 1, got 2"}},
		{"another value", CheckFailure{Check: base.Check, Test: base.Test, Detail: "foo_test.go:42: want 1, got 3"}},
		{"another message", CheckFailure{Check: base.Check, Test: base.Test, Detail: "foo_test.go:42: undefined: y"}},
		{"another duration-like word", CheckFailure{Check: base.Check, Test: base.Test, Detail: "foo_test.go:42: want 1, got 2 items"}},
	}
	sig := FailureSignature([]CheckFailure{base})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if FailureSignature([]CheckFailure{c.other}) == sig {
				t.Errorf("a real change kept the signature: %+v", c.other)
			}
		})
	}
	if FailureSignature([]CheckFailure{base, withTest(base, "pkg TestY")}) == sig {
		t.Error("an extra failing test kept the signature")
	}
}

func withTest(f CheckFailure, test string) CheckFailure {
	f.Test = test
	return f
}

func TestFailureSignatureIgnoresOrderAndRepeats(t *testing.T) {
	a := CheckFailure{Check: "test", Test: "A", Detail: "x"}
	b := CheckFailure{Check: "test", Test: "B", Detail: "y"}
	if FailureSignature([]CheckFailure{a, b}) != FailureSignature([]CheckFailure{b, a, b}) {
		t.Fatal("order or repeats changed the signature")
	}
	// The field separator keeps fields from running into one another.
	if FailureSignature([]CheckFailure{{Check: "ab", Test: "c"}}) == FailureSignature([]CheckFailure{{Check: "a", Test: "bc"}}) {
		t.Fatal("fields ran together")
	}
}

func TestFindingsSignature(t *testing.T) {
	a := Finding{ID: "b1", Severity: SeverityBlocker, Path: "x.go", Line: 10, Body: "nil deref here"}
	sig := FindingsSignature([]Finding{a})

	same := []struct {
		name string
		fs   []Finding
	}{
		{"reworded body and moved line", []Finding{{ID: "b1", Severity: SeverityBlocker, Path: "./x.go", Line: 31, Body: "still a nil deref"}}},
		{"nits added", []Finding{a, {ID: "n1", Severity: SeverityNit, Path: "y.go", Body: "style"}}},
		{"repeated", []Finding{a, a}},
	}
	for _, s := range same {
		if FindingsSignature(s.fs) != sig {
			t.Errorf("%s: changed the signature", s.name)
		}
	}
	differ := []struct {
		name string
		fs   []Finding
	}{
		{"another id", []Finding{{ID: "b2", Severity: SeverityBlocker, Path: "x.go", Body: a.Body}}},
		{"another path", []Finding{{ID: "b1", Severity: SeverityBlocker, Path: "y.go", Body: a.Body}}},
		{"another blocker too", []Finding{a, {ID: "b2", Severity: SeverityBlocker, Path: "x.go", Body: "z"}}},
		{"none", nil},
	}
	for _, d := range differ {
		if FindingsSignature(d.fs) == sig {
			t.Errorf("%s: kept the signature", d.name)
		}
	}
	// A finding with no id is known by its normalized body.
	noID := func(body string) Signature {
		return FindingsSignature([]Finding{{Severity: SeverityBlocker, Path: "x.go", Body: body}})
	}
	if noID("fails at x.go:10") != noID("fails at x.go:12") || noID("fails at x.go:10") == noID("leaks a goroutine") {
		t.Error("id-less findings are not keyed by their normalized body")
	}
	// Findings and failures never share a signature.
	if FindingsSignature(nil) == FailureSignature(nil) {
		t.Error("an empty findings signature equals an empty failures one")
	}
}

func TestSignatureShape(t *testing.T) {
	s := FailureSignature([]CheckFailure{{Check: "test"}})
	if !strings.HasPrefix(string(s), "v1:") || len(s) != len("v1:")+64 {
		t.Fatalf("%q", s)
	}
	if got := s.Short(); len(got) != len("v1:")+12 || !strings.HasPrefix(string(s), got) {
		t.Fatalf("Short() = %q", got)
	}
	if Signature("v1:ab").Short() != "v1:ab" {
		t.Fatal("a short signature should be kept whole")
	}
}
