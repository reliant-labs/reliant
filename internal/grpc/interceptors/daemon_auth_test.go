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
	validTokens map[string]string // rawToken -> userID
}

func (m *mockPATValidator) ValidatePAT(_ context.Context, rawToken string) (string, string, error) {
	if userID, ok := m.validTokens[rawToken]; ok {
		return userID, "pat-id", nil
	}
	return "", "", fmt.Errorf("invalid token")
}

func TestNewDaemonAuthInterceptorValidation(t *testing.T) {
	_, err := NewDaemonAuthInterceptor(nil)
	require.Error(t, err)

	interceptor, err := NewDaemonAuthInterceptor(&mockPATValidator{})
	require.NoError(t, err)
	require.NotNil(t, interceptor)
}

func TestDaemonAuthInterceptorAuthenticateSuccess(t *testing.T) {
	validator := &mockPATValidator{
		validTokens: map[string]string{
			"rlnt_pat_AbCdEfGhIjKlMnOpQrStUvWxYz123456": "user-123",
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
}

func TestDaemonAuthInterceptorAuthenticateRejectsMissingHeader(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(&mockPATValidator{})
	require.NoError(t, err)

	_, err = interceptor.authenticate(context.Background(), func(string) string { return "" })
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonAuthInterceptorAuthenticateRejectsInvalidHeader(t *testing.T) {
	interceptor, err := NewDaemonAuthInterceptor(&mockPATValidator{})
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
		validTokens: map[string]string{
			"rlnt_pat_ValidTokenHere1234567890abcdef": "user-123",
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
