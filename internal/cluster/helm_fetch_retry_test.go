package cluster

import "testing"

// TestTransientChartFetchFailure pins which chart-pull failures earn a retry.
// Rendering an OCI chart is a NETWORK operation, so a truncated transfer is a
// fault in the transfer, not in the chart — measured against a local
// inspection proxy the same pull failed roughly one attempt in three and
// succeeded on the next. A chart that is genuinely wrong must still fail on the
// first attempt: retrying it only delays the same verdict.
func TestTransientChartFetchFailure(t *testing.T) {
	transient := []string{
		`Error: failed to perform "Fetch" on source: Get "https://production.cloudfront.docker.com/...": unexpected EOF`,
		"Error: read tcp 10.0.0.1:52000->1.2.3.4:443: connection reset by peer",
		"Error: net/http: TLS handshake timeout",
		"Error: dial tcp: i/o timeout",
	}
	for _, out := range transient {
		if !transientChartFetchFailure(out) {
			t.Errorf("expected retry for a transfer-layer failure:\n%s", out)
		}
	}

	permanent := []string{
		`Error: chart "gateway-helm" version "v9.9.9" not found`,
		"Error: unauthorized: authentication required",
		"Error: parse error at (gateway-helm/templates/x.yaml:12): unexpected EOF-ish text",
		"Error: values don't meet the specifications of the schema",
		"",
	}
	// Every one of these must fail on the FIRST attempt — including the
	// template error that happens to contain the words "unexpected EOF",
	// which is exactly why the truncated-transfer signature requires
	// fetch context to count.
	for _, out := range permanent {
		if transientChartFetchFailure(out) {
			t.Errorf("a permanent chart failure must NOT be retried:\n%s", out)
		}
	}
}

// TestFirstLine keeps the retry progress message to one readable line.
func TestFirstLine(t *testing.T) {
	if got := firstLine("  line one\nline two\n"); got != "line one" {
		t.Errorf("firstLine = %q; want %q", got, "line one")
	}
	long := make([]rune, 250)
	for i := range long {
		long[i] = 'x'
	}
	if got := firstLine(string(long)); len([]rune(got)) != 101 { // 100 + ellipsis
		t.Errorf("firstLine did not truncate: %d runes", len([]rune(got)))
	}
}
