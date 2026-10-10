// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

type fakeRegistrationStream struct {
	sendErr error
	recvErr error
}

func (f *fakeRegistrationStream) Send(*reliantv1.DaemonMessage) error { return f.sendErr }
func (f *fakeRegistrationStream) Receive() (*reliantv1.ServerMessage, error) {
	return nil, f.recvErr
}

// connect-go reports a failed stream on Send as an error wrapping io.EOF and
// keeps the real cause on Receive. The registration error must carry it.
func TestSendRegistrationSurfacesReceiveError(t *testing.T) {
	sendErr := fmt.Errorf("write envelope: %w", io.EOF)
	realCause := connect.NewError(connect.CodeUnavailable, errors.New("dial tcp: lookup gateway.example.com: no such host"))

	err := sendRegistration(&fakeRegistrationStream{sendErr: sendErr, recvErr: realCause}, &reliantv1.DaemonMessage{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "no such host", "the real cause must be in the message")
	require.Contains(t, err.Error(), "write envelope: EOF", "the send error is kept for context")
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "the connect code must survive for fatal/retry classification")
}

func TestSendRegistrationKeepsPermissionDeniedFatal(t *testing.T) {
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("daemon id is owned by another user"))
	err := sendRegistration(&fakeRegistrationStream{sendErr: io.EOF, recvErr: denied}, &reliantv1.DaemonMessage{})
	require.Error(t, err)
	require.True(t, isFatalError(err), "a permission_denied found via Receive must still stop the daemon")
}

func TestSendRegistrationPassesThroughOtherCases(t *testing.T) {
	require.NoError(t, sendRegistration(&fakeRegistrationStream{}, &reliantv1.DaemonMessage{}))

	other := errors.New("boom")
	require.Equal(t, other, sendRegistration(&fakeRegistrationStream{sendErr: other, recvErr: errors.New("ignored")}, &reliantv1.DaemonMessage{}))

	// Receive gives nothing better than EOF: report the send error as before.
	sendErr := fmt.Errorf("write envelope: %w", io.EOF)
	require.Equal(t, sendErr, sendRegistration(&fakeRegistrationStream{sendErr: sendErr, recvErr: io.EOF}, &reliantv1.DaemonMessage{}))
}

// End to end against a real connect client: a gateway whose TLS certificate the
// daemon does not trust. Before this change the session error was only
// "write envelope: EOF"; the x509 failure is the part an operator needs.
func TestRunSessionNamesTLSFailureInsteadOfEOF(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler()) // self-signed; the default client does not trust it
	defer srv.Close()

	d := &daemonClient{
		gatewayURL:    srv.URL,
		gatewayClient: reliantv1connect.NewToolsDaemonServiceClient(&http.Client{Timeout: 10 * time.Second}, srv.URL, connect.WithGRPC()),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := d.runSession(ctx)

	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "sending daemon registration to "+srv.URL)
	require.True(t, strings.Contains(msg, "x509") || strings.Contains(msg, "certificate"),
		"the TLS failure must be named, got: %s", msg)
}
