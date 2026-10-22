// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
)

// serverAPIConfigEnv is every variable `server api` reads through
// ApiServerConfig.
var serverAPIConfigEnv = []string{"JWT_PUBLIC_KEY", "JWT_PUBLIC_KEY_FILE", "RELIANT_JWKS_URL", "ALLOWED_EMAIL_DOMAINS", "RELIANT_DISABLE_FORGE_API", "RELIANT_CONFIG",
	"AUTH_MODE", "AUTH_API_KEY", "AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM", "RELIANT_AUTHORIZATION_SERVER", "MCP_OAUTH_ISSUERS", "MAX_UPLOAD_SIZE", "RELIANT_WORKTREE_AUTO_ARCHIVE"}

// TestServerAPIConfigEnvContract pins that the variables `server api` now
// reads through ApiServerConfig reach apiOptions exactly as the flag
// defaults and raw reads they replaced did: the same names, the value
// verbatim, "" when unset or empty, and the flag winning over the variable.
// control-plane sets RELIANT_JWKS_URL on every reliant API server (dev, e2e
// and the shared lib-a env), so that name is a contract.
func TestServerAPIConfigEnvContract(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want apiOptions
	}{
		{name: "nothing set"},
		{name: "JWT_PUBLIC_KEY", env: map[string]string{"JWT_PUBLIC_KEY": "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----\n"},
			want: apiOptions{JWTPublicKey: "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----\n"}},
		{name: "JWT_PUBLIC_KEY_FILE", env: map[string]string{"JWT_PUBLIC_KEY_FILE": "/etc/reliant/jwt.pem"},
			want: apiOptions{JWTPublicKeyFile: "/etc/reliant/jwt.pem"}},
		{name: "RELIANT_JWKS_URL", env: map[string]string{"RELIANT_JWKS_URL": "https://example.supabase.co/auth/v1/.well-known/jwks.json"},
			want: apiOptions{JWKSURL: "https://example.supabase.co/auth/v1/.well-known/jwks.json"}},
		{name: "RELIANT_JWKS_URL empty is unset", env: map[string]string{"RELIANT_JWKS_URL": ""}},
		{name: "--jwks-url outranks RELIANT_JWKS_URL", env: map[string]string{"RELIANT_JWKS_URL": "https://env.example/jwks"},
			args: []string{"--jwks-url=https://flag.example/jwks"}, want: apiOptions{JWKSURL: "https://flag.example/jwks"}},
		{name: "--jwt-public-key outranks JWT_PUBLIC_KEY", env: map[string]string{"JWT_PUBLIC_KEY": "env-pem"},
			args: []string{"--jwt-public-key", "flag-pem"}, want: apiOptions{JWTPublicKey: "flag-pem"}},
		{name: "--jwt-public-key-file outranks JWT_PUBLIC_KEY_FILE", env: map[string]string{"JWT_PUBLIC_KEY_FILE": "/env.pem"},
			args: []string{"--jwt-public-key-file=/flag.pem"}, want: apiOptions{JWTPublicKeyFile: "/flag.pem"}},
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

// RELIANT_DISABLE_FORGE_API is the forge API's kill switch: opt-OUT, so unset
// or empty leaves the surface on. It is a bool, and a value that is not one
// stops the process: the raw read it replaced ignored an unrecognised value,
// which for a kill switch left the surface ON after an operator's typo.
func TestServerAPIDisableForgeAPI(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true, "TRUE": true} {
		t.Run("RELIANT_DISABLE_FORGE_API="+raw, func(t *testing.T) {
			isolateServerAPIConfigEnv(t)
			t.Setenv("RELIANT_DISABLE_FORGE_API", raw)
			assert.Equal(t, want, loadServerAPIOptions(t).DisableForgeAPI)
		})
	}
	for _, raw := range []string{"yes", "on", "maybe"} {
		t.Run("RELIANT_DISABLE_FORGE_API="+raw+" stops the process", func(t *testing.T) {
			isolateServerAPIConfigEnv(t)
			t.Setenv("RELIANT_DISABLE_FORGE_API", raw)
			cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
			require.NoError(t, err)
			var opts apiOptions
			err = loadServerAPIConfig(cmd, &opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "RELIANT_DISABLE_FORGE_API")
		})
	}
}

func isolateServerAPIConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range append(serverAPIConfigEnv, rootConfigEnv...) {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
	isolateServerRoleEnv(t)
}

// loadServerAPIOptions runs what `server api`'s RunE does with its typed
// config, after parsing args.
func loadServerAPIOptions(t *testing.T, args ...string) apiOptions {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	require.NoError(t, cmd.ParseFlags(args))
	var opts apiOptions
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
		require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "unexpected whitelist error: %v", err)
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
		var opts apiOptions
		err = loadServerAPIConfig(cmd, &opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jwksURL")
	})
}

// TestServerAPIAuthAndSurfaceSettings pins the API server's auth and surface
// settings to the names control-plane sets (AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM
// in lib/env.k, RELIANT_AUTHORIZATION_SERVER and MCP_OAUTH_ISSUERS in prod)
// and their defaults.
func TestServerAPIAuthAndSurfaceSettings(t *testing.T) {
	isolateServerAPIConfigEnv(t)
	opts := loadServerAPIOptions(t)
	assert.Equal(t, "supabase", opts.AuthMode)
	assert.False(t, opts.RequireEmailVerifiedClaim)
	assert.Empty(t, opts.AuthorizationServer)
	assert.Empty(t, opts.OAuthIssuers)
	assert.Equal(t, int64(10*1024*1024), opts.MaxUploadSize)
	assert.Equal(t, "dry-run", string(opts.WorktreeAutoArchive))

	t.Setenv("AUTH_MODE", "apikey")
	t.Setenv("AUTH_API_KEY", "shared-key")
	t.Setenv("AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM", "true")
	t.Setenv("RELIANT_AUTHORIZATION_SERVER", " https://admin.example ")
	t.Setenv("MCP_OAUTH_ISSUERS", "https://a.supabase.co/auth/v1, ,https://b.example")
	t.Setenv("MAX_UPLOAD_SIZE", "4096")
	t.Setenv("RELIANT_WORKTREE_AUTO_ARCHIVE", "on")
	opts = loadServerAPIOptions(t)
	assert.Equal(t, "apikey", opts.AuthMode)
	assert.Equal(t, "shared-key", opts.AuthAPIKey)
	assert.True(t, opts.RequireEmailVerifiedClaim)
	assert.Equal(t, "https://admin.example", opts.AuthorizationServer)
	assert.Equal(t, []string{"https://a.supabase.co/auth/v1", "https://b.example"}, opts.OAuthIssuers)
	assert.Equal(t, int64(4096), opts.MaxUploadSize)
	assert.Equal(t, "on", string(opts.WorktreeAutoArchive))
}

// A setting the API server cannot serve with stops it at load, naming it.
func TestServerAPIAuthMisconfigurationFailsBoot(t *testing.T) {
	for _, tc := range []struct {
		env       map[string]string
		wantInErr string
	}{
		{map[string]string{"AUTH_MODE": "apikey"}, "AUTH_API_KEY"},
		{map[string]string{"AUTH_MODE": "jwt"}, "auth_mode"},
		{map[string]string{"AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM": "yes"}, "AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM"},
		{map[string]string{"MAX_UPLOAD_SIZE": "10MB"}, "MAX_UPLOAD_SIZE"},
		{map[string]string{"MAX_UPLOAD_SIZE": "0"}, "MAX_UPLOAD_SIZE"},
		{map[string]string{"RELIANT_WORKTREE_AUTO_ARCHIVE": "true"}, "worktree_auto_archive"},
	} {
		t.Run(fmt.Sprint(tc.env), func(t *testing.T) {
			isolateServerAPIConfigEnv(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
			require.NoError(t, err)
			var opts apiOptions
			err = loadServerAPIConfig(cmd, &opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantInErr)
		})
	}
}
