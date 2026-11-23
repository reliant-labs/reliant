package cluster

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// kubectlReadAttempts / kubectlReadRetryDelay bound the retry of a read-only
// kubectl call that failed before it reached the API server. Vars so tests
// can shorten them.
var (
	kubectlReadAttempts   = 3
	kubectlReadRetryDelay = 2 * time.Second
)

// runKubectlRead runs one attempt of a read. A var so tests can stand in
// for kubectl in-process, on every OS, without a fake binary on PATH.
var runKubectlRead = func(cmd *exec.Cmd) error { return cmd.Run() }

// kubeTransportFailureSignatures name kubectl failures that come from the
// connection to the API server, never from the request: the server was not
// reached, so asking again is the whole fix. A NotFound, a Forbidden or a
// malformed request is answered by the server and is never retried.
var kubeTransportFailureSignatures = []string{
	"TLS handshake timeout",
	"i/o timeout",
	"connection reset by peer",
	"Unable to connect to the server",
	"http2: client connection lost",
	"net/http: request canceled while waiting for connection",
}

// transientKubeTransportFailure reports whether kubectl's output names a
// transport failure (kubeTransportFailureSignatures).
func transientKubeTransportFailure(output string) bool {
	for _, sig := range kubeTransportFailureSignatures {
		if strings.Contains(output, sig) {
			return true
		}
	}
	return false
}

// kubectlRead runs a READ-ONLY `kubectl <args>` against kctx and returns its
// stdout and stderr. A run that failed in transport (a TLS handshake timeout
// to a busy control plane, a reset connection) is retried with a short
// backoff, up to kubectlReadAttempts runs in all: one such blip failed a whole
// prod promotion at preflight while the same context answered a minute later
// (obs 449eddec). Callers pass only reads (get, api-resources), so a retry can
// never repeat a write.
func kubectlRead(ctx context.Context, kctx string, args ...string) (stdout, stderr []byte, err error) {
	for attempt := 1; ; attempt++ {
		cmd := kubectlCmd(ctx, kctx, args...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err = runKubectlRead(cmd)
		if err == nil || attempt >= kubectlReadAttempts || ctx.Err() != nil ||
			!transientKubeTransportFailure(errb.String()) {
			return out.Bytes(), errb.Bytes(), err
		}
		delay := time.Duration(attempt) * kubectlReadRetryDelay
		fmt.Fprintf(os.Stderr, "kubectl %s: %s (attempt %d/%d); retrying in %s\n",
			args[0], firstLine(errb.String()), attempt, kubectlReadAttempts, delay)
		select {
		case <-ctx.Done():
			return out.Bytes(), errb.Bytes(), err
		case <-time.After(delay):
		}
	}
}

// kubectlReadCombined is kubectlRead with stdout and stderr in one buffer,
// stderr last, for the callers that matched NotFound against
// CombinedOutput.
func kubectlReadCombined(ctx context.Context, kctx string, args ...string) ([]byte, error) {
	stdout, stderr, err := kubectlRead(ctx, kctx, args...)
	if err != nil {
		return append(append([]byte{}, stdout...), stderr...), err
	}
	return stdout, nil
}
