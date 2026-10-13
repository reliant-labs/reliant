// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/serverapi"
)

// serverAPIConfigEnv is every variable `server api` reads through
// ApiServerConfig.
var serverAPIConfigEnv = []string{"JWT_PUBLIC_KEY", "JWT_PUBLIC_KEY_FILE", "RELIANT_JWKS_URL", "ALLOWED_EMAIL_DOMAINS", "RELIANT_CONFIG"}

// TestServerAPIConfigEnvContract pins that the variables `server api` now
// reads through ApiServerConfig reach serverapi.Options exactly as the flag
// defaults and raw reads they replaced did: the same names, the value
// verbatim, "" when unset or empty, and the flag winning over the variable.
// control-plane sets RELIANT_JWKS_URL on every reliant API server (dev, e2e
// and the shared lib-a env), so that name is a contract.
func TestServerAPIConfigEnvContract(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want serverapi.Options
	}{
		{name: "nothing set"},
		{name: "JWT_PUBLIC_KEY", env: map[string]string{"JWT_PUBLIC_KEY": "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----\n"},
			want: serverapi.Options{JWTPublicKey: "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----\n"}},
		{name: "JWT_PUBLIC_KEY_FILE", env: map[string]string{"JWT_PUBLIC_KEY_FILE": "/etc/reliant/jwt.pem"},
			want: serverapi.Options{JWTPublicKeyFile: "/etc/reliant/jwt.pem"}},
		{name: "RELIANT_JWKS_URL", env: map[string]string{"RELIANT_JWKS_URL": "https://example.supabase.co/auth/v1/.well-known/jwks.json"},
			want: serverapi.Options{JWKSURL: "https://example.supabase.co/auth/v1/.well-known/jwks.json"}},
		{name: "RELIANT_JWKS_URL empty is unset", env: map[string]string{"RELIANT_JWKS_URL": ""}},
		{name: "--jwks-url outranks RELIANT_JWKS_URL", env: map[string]string{"RELIANT_JWKS_URL": "https://env.example/jwks"},
			args: []string{"--jwks-url=https://flag.example/jwks"}, want: serverapi.Options{JWKSURL: "https://flag.example/jwks"}},
		{name: "--jwt-public-key outranks JWT_PUBLIC_KEY", env: map[string]string{"JWT_PUBLIC_KEY": "env-pem"},
			args: []string{"--jwt-public-key", "flag-pem"}, want: serverapi.Options{JWTPublicKey: "flag-pem"}},
		{name: "--jwt-public-key-file outranks JWT_PUBLIC_KEY_FILE", env: map[string]string{"JWT_PUBLIC_KEY_FILE": "/env.pem"},
			args: []string{"--jwt-public-key-file=/flag.pem"}, want: serverapi.Options{JWTPublicKeyFile: "/flag.pem"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateServerAPIConfigEnv(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			opts := loadServerAPIOptions(t, tc.args...)
			assert.Equal(t, tc.want.JWTPublicKey, opts.JWTPublicKey, "JWTPublicKey")
			assert.Equal(t, tc.want.JWTPublicKeyFile, opts.JWTPublicKeyFile, "JWTPublicKeyFile")
			assert.Equal(t, tc.want.JWKSURL, opts.JWKSURL, "JWKSURL")
			assert.Empty(t, opts.AllowedEmailDomains, "AllowedEmailDomains")
		})
	}
}

// ALLOWED_EMAIL_DOMAINS used to be split on "," by hand and is now a repeated
// field, which the loader trims and drops blanks from. Its only reader, the
// domain whitelist, trims, lower-cases and drops blanks itself, so every value
// must admit and refuse exactly the same callers as the old split did.
func TestServerAPIAllowedEmailDomainsKeepTheirEffect(t *testing.T) {
	probes := []string{"user@a.com", "user@b.com", "user@B.COM", "user@c.com", "user@sub.a.com"}
	for _, raw := range []string{"", "a.com", "a.com,b.com", "a.com, B.com", " a.com ,", ",", "a.com,,b.com", "A.COM"} {
		t.Run("ALLOWED_EMAIL_DOMAINS="+raw, func(t *testing.T) {
			isolateServerAPIConfigEnv(t)
			t.Setenv("ALLOWED_EMAIL_DOMAINS", raw)
			opts := loadServerAPIOptions(t)

			var before []string // what server.go used to hand serverapi
			if raw != "" {
				before = strings.Split(raw, ",")
			}
			for _, email := range probes {
				assert.Equal(t, admits(t, before, email), admits(t, opts.AllowedEmailDomains, email),
					"%s: loaded %q, previously %q", email, opts.AllowedEmailDomains, before)
			}
		})
	}
}

func isolateServerAPIConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range append(serverAPIConfigEnv, rootConfigEnv...) {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
}

// loadServerAPIOptions runs what `server api`'s RunE does with its typed
// config, after parsing args.
func loadServerAPIOptions(t *testing.T, args ...string) serverapi.Options {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	require.NoError(t, cmd.ParseFlags(args))
	var opts serverapi.Options
	require.NoError(t, loadServerAPIConfig(cmd, &opts))
	return opts
}

// admits reports whether the API server's domain whitelist, built from
// domains, lets a caller signed in as email through.
func admits(t *testing.T, domains []string, email string) bool {
	t.Helper()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-123")
	ctx = context.WithValue(ctx, auth.UserEmailContextKey, email)
	call := interceptors.NewDomainWhitelistInterceptor(domains).WrapUnary(
		func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, nil })
	_, err := call(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "unexpected whitelist error: %v", err)
		return false
	}
	return true
}

// `server api` loads two configs, the root ReliantConfig and its own
// ApiServerConfig, from ONE file: each takes its own top-level keys, and a key
// neither declares stops the process rather than being dropped.
func TestServerAPISharedConfigFile(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "reliant.yaml")
	require.NoError(t, os.WriteFile(shared, []byte(
		"log:\n  level: warn\njwksUrl: https://file.example/jwks\nallowedEmailDomains: [a.com, b.com]\n"), 0o600))
	typo := filepath.Join(dir, "typo.yaml")
	require.NoError(t, os.WriteFile(typo, []byte("jwksURL: https://file.example/jwks\n"), 0o600))

	t.Run("each config takes its own keys", func(t *testing.T) {
		isolateServerAPIConfigEnv(t)
		t.Setenv("RELIANT_CONFIG", shared)
		opts := loadServerAPIOptions(t)
		assert.Equal(t, "warn", opts.Log.Level)
		assert.Equal(t, "https://file.example/jwks", opts.JWKSURL)
		assert.Equal(t, []string{"a.com", "b.com"}, opts.AllowedEmailDomains)
	})
	t.Run("env outranks the file", func(t *testing.T) {
		isolateServerAPIConfigEnv(t)
		t.Setenv("RELIANT_CONFIG", shared)
		t.Setenv("RELIANT_JWKS_URL", "https://env.example/jwks")
		assert.Equal(t, "https://env.example/jwks", loadServerAPIOptions(t).JWKSURL)
	})
	t.Run("a key no loaded config declares is an error", func(t *testing.T) {
		isolateServerAPIConfigEnv(t)
		cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
		require.NoError(t, err)
		require.NoError(t, cmd.ParseFlags([]string{"--config", typo}))
		var opts serverapi.Options
		err = loadServerAPIConfig(cmd, &opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jwksURL")
	})
}
