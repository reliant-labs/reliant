package interceptors

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/stretchr/testify/require"
)

type mockPATValidator struct {
	validTokens map[string]mockPATResult // rawToken -> result
}

type mockPATResult struct {
	userID   string
	daemonID string
}

func (m *mockPATValidator) ValidatePAT(_ context.Context, rawToken string) (string, string, string, error) {
	if result, ok := m.validTokens[rawToken]; ok {
		return result.userID, "pat-id", result.daemonID, nil
	}
	return "", "", "", fmt.Errorf("invalid token")
}

func TestNewDaemonAuthInterceptorValidation(t *testing.T) {
	_, err := NewDaemonAuthInterceptor(nil)
	require.Error(t, err)

	interceptor, err := NewDaemonAuthInterceptor(&mockPATValidator{validTokens: map[string]mockPATResult{}})
	require.NoError(t, err)
	require.NotNil(t, interceptor)
}

func TestDaemonAuthInterceptorAuthenticateSuccess(t *testing.T) {
	validator := &mockPATValidator{
		validTokens: map[string]mockPATResult{
			"rlnt_pat_AbCdEfGhIjKlMnOpQrStUvWxYz123456": {userID: "user-123", daemonID: "daemon-42"},
		},
	}
	interceptor, err := NewDaemonAuthInterceptor(validator)
	require.NoError(t, err)

	ctx, err := interceptor.authenticate(context.Background(), func(key string) string {
		if key == "Authorization" {
			return "Bearer rlnt_pat_AbCdEfGhIjKlMnOpQrStUvWxYz123456"
		}
		return ""
	})
	require.NoError(t, err)

	userID, ok := auth.GetUserIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "user-123", userID)

	daemonID := auth.GetDaemonIDFromContext(ctx)
	require.Equal(t, "daemon-42", daemonID)
}

func TestDaemonAuthInterceptorAuthenticateUnboundPAT(t *testing.T) {
	validator := &mockPATValidator{
		validTokens: map[string]mockPATResult{
			"rlnt_pat_AbCdEfGhIjKlMnOpQrStUvWxYz123456": {userID: "user-123", daemonID: ""},
		},
	}
	interceptor, err := NewDaemonAuthInterceptor(validator)
	require.NoError(t, err)

	ctx, err := interceptor.authenticate(context.Background(), func(key string) string {
		if key == "Authorization" {
			return "Bearer rlnt_pat_AbCdEfGhIjKlMnOpQrStUvWxYz123456"
		}
		return ""
	})
	require.NoError(t, err)

	userID, ok := auth.GetUserIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "user-123", userID)

	// Unbound PAT should not inject daemon_id into context.
	daemonID := auth.GetDaemonIDFromContext(ctx)
	require.Equal(t, "", daemonID)
}

func TestDaemonAuthInterceptorAuthenticateRejectsMissingHeader(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(&mockPATValidator{validTokens: map[string]mockPATResult{}})
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), func(string) string { return "" })
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorAuthenticateRejectsInvalidHeader(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(&mockPATValidator{validTokens: map[string]mockPATResult{}})
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), func(key string) string {
		if key == "Authorization" {
			return "some-token-without-bearer"
		}
		return ""
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorAuthenticateRejectsWrongToken(t *testing.T) {
	validator := &mockPATValidator{
		validTokens: map[string]mockPATResult{
			"rlnt_pat_ValidTokenHere1234567890abcdef": {userID: "user-123"},
		},
	}
	interceptor, err := NewDaemonAuthInterceptor(validator)
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), func(key string) string {
		if key == "Authorization" {
			return "Bearer rlnt_pat_WrongTokenHere1234567890abcdef"
		}
		return ""
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
