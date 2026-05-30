// Copyright (c) 2025 Reliant Labs

package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotateReliantAPIKey_PersistsNewKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	const userID = "test-user"
	const oldKey = "sk-old-virtual"
	const newKey = "sk-rotated-virtual"

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "reliant", oldKey))

	var sawOldKeyInDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/key/delete":
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			sawOldKeyInDelete = string(body) != "" && contains(string(body), oldKey)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"deleted":1}`))
		case "/v1/key/generate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"` + newKey + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("RELIANT_API_BASE_URL", server.URL+"/v1")
	t.Setenv("RELIANT_ADMIN_API_KEY", "sk-admin-test")
	t.Setenv("LITELLM_MASTER_KEY", "")

	svc := NewSettingsService(repo, nil)
	resp, err := svc.RotateReliantAPIKey(ctx, connect.NewRequest(&reliantv1.RotateReliantAPIKeyRequest{}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Msg.Success)
	assert.True(t, sawOldKeyInDelete, "delete should have been called with the old key")

	stored, err := repo.GetProviderAPIKey(ctx, userID, "reliant")
	require.NoError(t, err)
	assert.Equal(t, newKey, stored)
}

func TestRotateReliantAPIKey_FailsWhenNoExistingKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := NewSettingsService(repo, nil)

	_, err := svc.RotateReliantAPIKey(ctx, connect.NewRequest(&reliantv1.RotateReliantAPIKeyRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
