// Copyright (c) 2025 Reliant Labs

package drivers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMintReliantUserAPIKey_Success(t *testing.T) {
	var capturedAuth string
	var capturedBody map[string]string
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":"sk-minted-virtual-key"}`))
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")
	t.Setenv("LITELLM_MASTER_KEY", "")

	key, err := MintReliantUserAPIKey(context.Background(), "user-123")
	require.NoError(t, err)
	assert.Equal(t, "sk-minted-virtual-key", key)
	assert.Equal(t, "Bearer sk-admin-test", capturedAuth)
	assert.Equal(t, "/v1/key/generate", capturedPath)
	assert.Equal(t, "user-123", capturedBody["user_id"])
	assert.Equal(t, "user-user-123", capturedBody["key_alias"])
}

func TestMintReliantUserAPIKey_FallsBackToLiteLLMMasterKey(t *testing.T) {
	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":"sk-from-master"}`))
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "")
	t.Setenv("LITELLM_MASTER_KEY", "sk-master-fallback")

	key, err := MintReliantUserAPIKey(context.Background(), "user-456")
	require.NoError(t, err)
	assert.Equal(t, "sk-from-master", key)
	assert.Equal(t, "Bearer sk-master-fallback", capturedAuth)
}

func TestMintReliantUserAPIKey_NonSuccessStatusReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")

	key, err := MintReliantUserAPIKey(context.Background(), "user-789")
	require.Error(t, err)
	assert.Empty(t, key)
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "forbidden")
}

func TestMintReliantUserAPIKey_MissingAdminCredentialReturnsError(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "")
	t.Setenv("LITELLM_MASTER_KEY", "")

	key, err := MintReliantUserAPIKey(context.Background(), "user-no-creds")
	require.Error(t, err)
	assert.Empty(t, key)
	assert.Contains(t, err.Error(), "RELIANT_ADMIN_API_KEY")
	assert.False(t, hit, "should not call the network when credentials are missing")
}

func TestMintReliantUserAPIKey_EmptyUserIDReturnsErrorBeforeCall(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")

	key, err := MintReliantUserAPIKey(context.Background(), "   ")
	require.Error(t, err)
	assert.Empty(t, key)
	assert.True(t, strings.Contains(err.Error(), "user_id"))
	assert.False(t, hit, "should not call the network when user_id is empty")
}

func TestRotateReliantUserAPIKey_DeletesOldAndMintsNew(t *testing.T) {
	var deleteCalled bool
	var deletedKeys []string
	var mintCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/key/delete":
			deleteCalled = true
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Keys []string `json:"keys"`
			}
			_ = json.Unmarshal(body, &payload)
			deletedKeys = payload.Keys
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"deleted":1}`))
		case "/v1/key/generate":
			mintCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"sk-rotated-new"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")
	t.Setenv("LITELLM_MASTER_KEY", "")

	key, err := RotateReliantUserAPIKey(context.Background(), "user-rotate-1", "sk-old-key")
	require.NoError(t, err)
	assert.Equal(t, "sk-rotated-new", key)
	assert.True(t, deleteCalled, "delete should be called when old key provided")
	assert.True(t, mintCalled, "mint should be called")
	assert.Equal(t, []string{"sk-old-key"}, deletedKeys)
}

func TestRotateReliantUserAPIKey_DeleteFailureStillMints(t *testing.T) {
	var mintCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/key/delete":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"transient"}`))
		case "/v1/key/generate":
			mintCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"sk-rotated-anyway"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")

	key, err := RotateReliantUserAPIKey(context.Background(), "user-rotate-2", "sk-old-stale")
	require.NoError(t, err)
	assert.Equal(t, "sk-rotated-anyway", key)
	assert.True(t, mintCalled, "mint must run even when delete fails")
}

func TestRotateReliantUserAPIKey_MintFailureReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/key/delete":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"deleted":1}`))
		case "/v1/key/generate":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")

	key, err := RotateReliantUserAPIKey(context.Background(), "user-rotate-3", "sk-old-key")
	require.Error(t, err)
	assert.Empty(t, key)
	assert.Contains(t, err.Error(), "403")
}

func TestRotateReliantUserAPIKey_EmptyOldKeySkipsDelete(t *testing.T) {
	var deleteCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/key/delete":
			deleteCalled = true
			w.WriteHeader(http.StatusOK)
		case "/v1/key/generate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"sk-fresh-only"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")

	key, err := RotateReliantUserAPIKey(context.Background(), "user-rotate-4", "   ")
	require.NoError(t, err)
	assert.Equal(t, "sk-fresh-only", key)
	assert.False(t, deleteCalled, "delete must not be attempted when old key is empty")
}

func TestMintReliantUserAPIKey_EmptyKeyInResponseReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key":""}`))
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")

	key, err := MintReliantUserAPIKey(context.Background(), "user-empty-key")
	require.Error(t, err)
	assert.Empty(t, key)
	assert.Contains(t, err.Error(), "empty key")
}
