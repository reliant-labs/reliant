// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A daemon stream reaped for going silent ends with the code the daemon
// redials on (Unavailable), but it is the other end going away — not a
// server fault — so the stream's "stream failed" line is not an ERROR issue.
func TestDaemonStreamSilentIsUnavailableButCanceledClass(t *testing.T) {
	if code := svcerr.Code(errDaemonStreamSilent); code != connect.CodeUnavailable {
		t.Errorf("code = %v, want Unavailable", code)
	}
	if c := svcerr.Classify(errDaemonStreamSilent); c != svcerr.ClassCanceled {
		t.Errorf("class = %v, want canceled", c)
	}
}
