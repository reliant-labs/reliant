package cluster

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeFlakyKubectl stands in for kubectl in-process (runKubectlRead): its
// first `fails` runs write `failure` to stderr and fail, later runs print
// the keys tls.crt and tls.key to stdout. Nothing is exec'd, so the result
// is the same on every OS: a fake binary on PATH is not found on Windows,
// where the runner's real kubectl then dials a context that does not exist.
// It returns a func that counts the runs so far.
func fakeFlakyKubectl(t *testing.T, fails int, failure string) func() int {
	t.Helper()
	var runs int
	prev := runKubectlRead
	runKubectlRead = func(cmd *exec.Cmd) error {
		runs++
		if runs <= fails {
			_, _ = io.WriteString(cmd.Stderr, failure+"\n")
			return errors.New("exit status 1")
		}
		_, _ = io.WriteString(cmd.Stdout, "tls.crt\ntls.key\n")
		return nil
	}
	t.Cleanup(func() { runKubectlRead = prev })
	return func() int { return runs }
}

func shortKubectlReadRetry(t *testing.T) {
	t.Helper()
	prev := kubectlReadRetryDelay
	kubectlReadRetryDelay = time.Millisecond
	t.Cleanup(func() { kubectlReadRetryDelay = prev })
}

// TestPreflightSecretReadRetriesTransportFailure pins obs 449eddec: one TLS
// handshake timeout reading observability/otel-agent-token failed a whole prod
// promotion at preflight, though the context answered a minute later.
func TestPreflightSecretReadRetriesTransportFailure(t *testing.T) {
	shortKubectlReadRetry(t)
	runs := fakeFlakyKubectl(t, 1, "Unable to connect to the server: net/http: TLS handshake timeout")
	keys, exists, err := KubectlSecretGetter{}.GetSecretKeys(context.Background(), "prod", "observability", "otel-agent-token")
	if err != nil || !exists {
		t.Fatalf("GetSecretKeys = exists %v, err %v; want the retried read to succeed", exists, err)
	}
	if _, ok := keys["tls.crt"]; !ok || runs() != 2 {
		t.Fatalf("keys %v after %d runs; want tls.crt after 2", keys, runs())
	}
}

func TestPreflightSecretReadGivesUpAfterBoundedRetries(t *testing.T) {
	shortKubectlReadRetry(t)
	runs := fakeFlakyKubectl(t, 99, "Unable to connect to the server: net/http: TLS handshake timeout")
	_, _, err := KubectlSecretGetter{}.GetSecretKeys(context.Background(), "prod", "observability", "otel-agent-token")
	if err == nil || !strings.Contains(err.Error(), "TLS handshake timeout") {
		t.Fatalf("err = %v; want the transport failure after the last attempt", err)
	}
	if runs() != kubectlReadAttempts {
		t.Fatalf("%d runs; want %d", runs(), kubectlReadAttempts)
	}
}

// An answer from the API server is never retried: NotFound is "missing", and
// a refusal is the verdict.
func TestPreflightSecretReadDoesNotRetryServerAnswers(t *testing.T) {
	shortKubectlReadRetry(t)
	for name, tc := range map[string]struct {
		failure    string
		wantExists bool
		wantErr    bool
	}{
		"not found": {failure: `Error from server (NotFound): secrets "otel-agent-token" not found`},
		"forbidden": {failure: `Error from server (Forbidden): secrets "otel-agent-token" is forbidden`, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			runs := fakeFlakyKubectl(t, 99, tc.failure)
			_, exists, err := KubectlSecretGetter{}.GetSecretKeys(context.Background(), "prod", "observability", "otel-agent-token")
			if exists != tc.wantExists || (err != nil) != tc.wantErr {
				t.Fatalf("exists %v err %v; want exists %v err %v", exists, err, tc.wantExists, tc.wantErr)
			}
			if runs() != 1 {
				t.Fatalf("%d runs; a server answer must not be retried", runs())
			}
		})
	}
}

func TestPreflightCombinedReadsRetryToo(t *testing.T) {
	shortKubectlReadRetry(t)
	runs := fakeFlakyKubectl(t, 1, "read tcp 10.0.0.1:443: i/o timeout")
	if _, err := (KubectlServedKinds{}).ServedKinds(context.Background(), "prod"); err != nil {
		t.Fatalf("ServedKinds: %v", err)
	}
	if runs() != 2 {
		t.Fatalf("%d runs; want 2", runs())
	}
}
