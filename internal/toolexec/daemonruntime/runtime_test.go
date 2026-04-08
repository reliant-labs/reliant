package daemonruntime

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUTF8Sanitization(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool // whether input is already valid UTF-8
	}{
		{
			name:  "valid ASCII",
			input: "hello world",
			valid: true,
		},
		{
			name:  "valid UTF-8 with unicode",
			input: "hello 🌍 world café",
			valid: true,
		},
		{
			name:  "invalid UTF-8 bytes",
			input: "hello \x80\x81\x82 world",
			valid: false,
		},
		{
			name:  "mixed valid and invalid",
			input: "grep output: \xff\xfe some text \x80",
			valid: false,
		},
		{
			name:  "empty string",
			input: "",
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitized := strings.ToValidUTF8(tt.input, "\uFFFD")
			assert.True(t, utf8.ValidString(sanitized), "sanitized string must be valid UTF-8")

			if tt.valid {
				assert.Equal(t, tt.input, sanitized, "valid input should be unchanged")
			} else {
				assert.NotEqual(t, tt.input, sanitized, "invalid input should be modified")
				assert.Contains(t, sanitized, "\uFFFD", "invalid bytes should be replaced with U+FFFD")
			}
		})
	}
}

func TestNewDaemonClient_BindsLocalMCPContextBinder(t *testing.T) {
	t.Setenv("DAEMON_WORKING_DIR", t.TempDir())

	client, err := newDaemonClient(bootstrap.DaemonBootstrapConfig{
		UserID:    "test-user",
		AuthToken: "test-token",
		GRPCURL:   "http://127.0.0.1:9999",
		TLSMode:   bootstrap.TLSModeH2C,
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.localExecutor)
	require.NotNil(t, client.mcpManager)

	execValue := reflect.ValueOf(client.localExecutor).Elem()
	binderField := execValue.FieldByName("mcpBinder")
	require.True(t, binderField.IsValid(), "local executor should expose mcpBinder field")
	assert.False(t, binderField.IsNil(), "daemon local executor should have an MCP binder attached")
}
