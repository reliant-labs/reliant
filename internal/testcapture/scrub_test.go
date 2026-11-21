package testcapture

import (
	"strings"
	"testing"
)

// The credential-shaped inputs are assembled at run time, so no literal in
// this file looks like a live credential or key to a secret scanner; each
// still has exactly the shape its pattern matches.
var (
	fakeGitHubToken = "ghp" + "_" + strings.Repeat("a", 26) + "0123"
	fakeGitHubPAT   = "github" + "_pat_" + "11ABCDEFG0123456789_abcdef"
	fakeSlackToken  = "xox" + "b-" + "1234567890-abcdef"
	fakeAWSKeyID    = "AK" + "IA" + strings.Repeat("ABCD", 4)
	fakeGCPKey      = "AI" + "za" + strings.Repeat("a", 26) + "012345678"
	fakeStripeKey   = "sk" + "_live_" + "abcdefghij12"
	fakeOpenAIKey   = "sk" + "-" + strings.Repeat("a", 24)
	fakeJWT         = "ey" + "JhbGciOiJI." + "ey" + "JzdWIiOiIx." + "SflKxwRJSMeK"
)

// pem is a PEM private key boundary line: five dashes, the edge (BEGIN or
// END), the key kind, "PRIVATE KEY", five dashes.
func pem(edge, kind string) string {
	return strings.Repeat("-", 5) + edge + " " + kind + "PRIVATE " + "KEY" + strings.Repeat("-", 5)
}

func TestScrubberLineRedactsEveryPattern(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"token " + fakeGitHubToken, "token [redacted]"},
		{"pat " + fakeGitHubPAT, "pat [redacted]"},
		{"slack " + fakeSlackToken, "slack [redacted]"},
		{"aws " + fakeAWSKeyID + " done", "aws [redacted] done"},
		{"gcp " + fakeGCPKey, "gcp [redacted]"},
		{"stripe " + fakeStripeKey, "stripe [redacted]"},
		{"openai " + fakeOpenAIKey, "openai [redacted]"},
		{"jwt " + fakeJWT, "jwt [redacted-jwt]"},
		{"dial postgres://app:s3cret@db:5432/x", "dial postgres://[redacted]@db:5432/x"},
		// Both the bearer and the authorization patterns fire, as in sed.
		{"Authorization: Bearer abcdefghijkl1234", "Authorization: [redacted] [redacted]"},
		{"curl -H 'bearer abcdefghijkl1234'", "curl -H 'bearer [redacted]'"},
		{`password="hunter22"`, `password="[redacted]"`},
		{"API_KEY: abcd1234", "API_KEY: [redacted]"},
		{"client-secret=abcd,next", "client-secret=[redacted],next"},
		// Not secrets: left exactly as they were.
		{"--- FAIL: TestPassword (0.00s)", "--- FAIL: TestPassword (0.00s)"},
		{"ok  \texample.com/x\t0.01s", "ok  \texample.com/x\t0.01s"},
		{"token: abc", "token: abc"},
	} {
		var s Scrubber
		got, drop := s.Line(tc.in)
		if drop || got != tc.want {
			t.Errorf("Line(%q) = %q, %v; want %q", tc.in, got, drop, tc.want)
		}
	}
}

func TestScrubberReplacesAPrivateKeyBlockWithOneLine(t *testing.T) {
	t.Parallel()
	var s Scrubber
	in := "before\n" + pem("BEGIN", "RSA ") + "\nMIIEow\nIBAAKC\n" + pem("END", "RSA ") + "\nafter\n"
	if got, want := s.Text(in), "before\n[redacted private key]\nafter\n"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	// The block spans lines, so the state carries across calls, as it does
	// across the events of one stream.
	var ev Scrubber
	var got string
	for _, line := range []string{pem("BEGIN", "") + "\n", "abc\n", pem("END", "") + "\n", "next\n"} {
		got += ev.Text(line)
	}
	if want := "[redacted private key]\nnext\n"; got != want {
		t.Fatalf("across calls = %q, want %q", got, want)
	}
	// A one-line key ends where it began.
	var one Scrubber
	if got, want := one.Text("k="+pem("BEGIN", "EC ")+"abc"+pem("END", "EC ")+"\nnext\n"), "[redacted private key]\nnext\n"; got != want {
		t.Fatalf("one-line key = %q, want %q", got, want)
	}
}

func TestScrubberTextKeepsLineEndings(t *testing.T) {
	t.Parallel()
	var s Scrubber
	for _, in := range []string{"", "a", "a\n", "a\nb", "a\n\nb\n"} {
		if got := s.Text(in); got != in {
			t.Errorf("Text(%q) = %q, want it unchanged", in, got)
		}
	}
}
