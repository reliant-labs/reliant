package interceptors

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/stretchr/testify/require"
)

func contextWithEmail(email string) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, auth.UserIDContextKey, "user-123")
	ctx = context.WithValue(ctx, auth.UserEmailContextKey, email)
	return ctx
}

func TestDomainWhitelist_EmptyAllowedList(t *testing.T) {
	i := NewDomainWhitelistInterceptor(nil)
	require.NoError(t, i.checkDomain(contextWithEmail("anyone@gmail.com")))

	i2 := NewDomainWhitelistInterceptor([]string{})
	require.NoError(t, i2.checkDomain(contextWithEmail("anyone@gmail.com")))
}

func TestDomainWhitelist_AllowedDomain(t *testing.T) {
	i := NewDomainWhitelistInterceptor([]string{"reliantlabs.io"})
	require.NoError(t, i.checkDomain(contextWithEmail("user@reliantlabs.io")))
}

func TestDomainWhitelist_RejectedDomain(t *testing.T) {
	i := NewDomainWhitelistInterceptor([]string{"reliantlabs.io"})
	err := i.checkDomain(contextWithEmail("user@gmail.com"))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestDomainWhitelist_CaseInsensitive(t *testing.T) {
	i := NewDomainWhitelistInterceptor([]string{"ReliantLabs.IO"})
	require.NoError(t, i.checkDomain(contextWithEmail("user@reliantlabs.io")))
	require.NoError(t, i.checkDomain(contextWithEmail("user@RELIANTLABS.IO")))
	require.NoError(t, i.checkDomain(contextWithEmail("user@ReliantLabs.Io")))
}

func TestDomainWhitelist_NoEmailInContext(t *testing.T) {
	i := NewDomainWhitelistInterceptor([]string{"reliantlabs.io"})

	// No email at all
	require.NoError(t, i.checkDomain(context.Background()))

	// Empty email
	ctx := context.WithValue(context.Background(), auth.UserEmailContextKey, "")
	require.NoError(t, i.checkDomain(ctx))
}

func TestDomainWhitelist_MultipleDomains(t *testing.T) {
	i := NewDomainWhitelistInterceptor([]string{"reliantlabs.io", "example.com"})
	require.NoError(t, i.checkDomain(contextWithEmail("user@reliantlabs.io")))
	require.NoError(t, i.checkDomain(contextWithEmail("user@example.com")))
	require.Error(t, i.checkDomain(contextWithEmail("user@other.com")))
}

func TestDomainWhitelist_WhitespaceInDomains(t *testing.T) {
	i := NewDomainWhitelistInterceptor([]string{" reliantlabs.io ", "  example.com"})
	require.NoError(t, i.checkDomain(contextWithEmail("user@reliantlabs.io")))
	require.NoError(t, i.checkDomain(contextWithEmail("user@example.com")))
}

func TestDomainWhitelist_MalformedEmail(t *testing.T) {
	i := NewDomainWhitelistInterceptor([]string{"reliantlabs.io"})
	// No @ sign — passes through (treated as no parseable domain)
	require.NoError(t, i.checkDomain(contextWithEmail("nodomain")))
}
