package logging

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiagnosticInspectsOriginalAssignmentNotPartialMarker(t *testing.T) {
	for _, text := range []string{
		`password="prefix\"opaque-secret-value"`,
		`password='prefix'\''opaque-secret-value'`,
		`password="prefix\\opaque-secret-value"`,
		`password="prefix\\nopaque-secret-value"`,
		`password="prefix;opaque-secret-value"`,
		`password=[REDACTED]opaque-secret-value`,
		`password=opaque-secret-value`,
		`Authorization: custom "prefix\"opaque-secret-value"`,
	} {
		require.Equal(t, "[REDACTED: credential-bearing diagnostic omitted]", RedactDiagnosticText(text), text)
	}
	require.Equal(t, "compiler: expected closing delimiter", RedactDiagnosticText("compiler: expected closing delimiter"))
}

func TestRedactDiagnosticSecretFields(t *testing.T) {
	for _, text := range []string{
		`{"client_secret":"topsecret","message":"safe"}`,
		`{"api_key":"prefix\"topsecret","message":"safe"}`,
		"CLIENT_SECRET=topsecret safe",
		"-----BEGIN PRIVATE KEY-----\ntopsecret\n-----END PRIVATE KEY----- safe",
	} {
		got := RedactText(text)
		require.NotContains(t, got, "topsecret")
		require.Contains(t, got, "[REDACTED]")
		require.Contains(t, got, "safe")
	}
}
