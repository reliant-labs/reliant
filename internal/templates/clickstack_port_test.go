package templates_test

import (
	"strings"
	"testing"
)

// composePortPublished reports whether `docker compose port <service> <port>`
// printed a published host binding. An unpublished port prints as port 0, and
// the host part varies by Compose release (":0", "invalid IP:0",
// "0.0.0.0:0"), so the port after the last colon decides, never the prefix.
func composePortPublished(out string) bool {
	out = strings.TrimSpace(out)
	if out == "" {
		return false
	}
	i := strings.LastIndex(out, ":")
	return i < 0 || out[i+1:] != "0"
}

func TestComposePortPublished(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"", false},
		{"\n", false},
		{":0", false},
		{"invalid IP:0", false},
		{"invalid IP:0\n", false},
		{"0.0.0.0:0", false},
		{"0.0.0.0:8123", true},
		{"127.0.0.1:49153", true},
		{"[::]:8123", true},
		{"0.0.0.0:80", true},
		{"0.0.0.0:10", true},
	} {
		if got := composePortPublished(tc.out); got != tc.want {
			t.Errorf("composePortPublished(%q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}
