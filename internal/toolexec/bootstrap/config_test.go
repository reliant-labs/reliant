package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGatewayURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "http passes through", raw: "http://localhost:29190", want: "http://localhost:29190"},
		{name: "https passes through", raw: "https://gateway.example.com", want: "https://gateway.example.com"},
		{
			// `forge cluster urls` prints this form; it must dial, not die.
			name: "grpc becomes plaintext h2c",
			raw:  "grpc://localhost:29190",
			want: "http://localhost:29190",
		},
		{name: "grpcs becomes TLS", raw: "grpcs://gateway.example.com:443", want: "https://gateway.example.com:443"},
		{name: "surrounding whitespace tolerated", raw: "  grpc://localhost:29190\n", want: "http://localhost:29190"},
		{name: "empty is rejected", raw: "", wantErr: "missing daemon gateway URL"},
		{
			// A bare host:port parses as scheme "localhost" — it must be named
			// as unsupported rather than dialed into a silent EOF.
			name:    "scheme-less address is rejected naming the working schemes",
			raw:     "localhost:29190",
			wantErr: "use http://",
		},
		{name: "unknown scheme is rejected", raw: "ws://localhost:29190", wantErr: "cannot be dialed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeGatewayURL(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// Validate must reject an undialable gateway URL at boot. Accepting it and
// letting the stream fail later is what produced a daemon process that ran for
// hours without ever connecting.
func TestValidateRejectsUndialableGatewayURL(t *testing.T) {
	err := DaemonBootstrapConfig{
		AuthToken: "rlnt_pat_x",
		GRPCURL:   "ws://localhost:29190",
		TLSMode:   TLSModeH2C,
	}.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be dialed")
}

func TestValidateAcceptsGRPCScheme(t *testing.T) {
	require.NoError(t, DaemonBootstrapConfig{
		AuthToken: "rlnt_pat_x",
		GRPCURL:   "grpc://localhost:29190",
		TLSMode:   TLSModeH2C,
	}.Validate())
}
