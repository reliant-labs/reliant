//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// checkProvider implements the external provider boundary only. The actual
// CP journal, credential store/service, internal handler and bridge run unchanged.
type checkProvider struct {
	mu     sync.Mutex
	writes int
	check  map[string]any
	t      *testing.T
}

func (f *checkProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	var out any
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/example":
		if r.Header.Get("Authorization") != "Bearer ghp_futurechat_b_fixture_owner" {
			f.t.Error("CP did not use its actual persisted owner credential")
		}
		out = map[string]any{"id": 42, "full_name": "acme/example", "permissions": map[string]bool{"push": true}}
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/example/installation":
		out = map[string]any{"id": 8, "app_id": 7}
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/8/access_tokens":
		var scope struct {
			RepositoryIDs []int64           `json:"repository_ids"`
			Permissions   map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&scope); err != nil || len(scope.RepositoryIDs) != 1 || scope.RepositoryIDs[0] != 42 || len(scope.Permissions) != 1 || scope.Permissions["checks"] != "write" {
			f.t.Error("bridge broadened fixture installation token scope")
		}
		status = http.StatusCreated
		out = map[string]any{"token": "ghs_futurechat_b_fixture_checks", "expires_at": time.Now().Add(time.Hour), "permissions": map[string]string{"checks": "write", "metadata": "read"}}
	case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
		out = map[string]any{"total_count": 1, "repositories": []map[string]any{{"id": 42, "full_name": "acme/example"}}}
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/example/check-runs":
		var check map[string]any
		if err := json.NewDecoder(r.Body).Decode(&check); err != nil {
			f.t.Error("invalid provider check payload")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if check["head_sha"] != strings.Repeat("a", 40) || check["name"] != "ReliantReview" || check["external_id"] != "fixture/assignment/1" || check["conclusion"] != "success" {
			f.t.Error("check mutation was not exact-head, exact-intent review")
		}
		f.writes++
		check["id"] = int64(99)
		check["url"] = "https://api.github.com/repos/acme/example/check-runs/99"
		check["app"] = map[string]any{"id": 7}
		f.check = check
		status, out = http.StatusCreated, check
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/example/commits/"+strings.Repeat("a", 40)+"/check-runs":
		checks := []map[string]any{}
		if f.check != nil {
			checks = append(checks, f.check)
		}
		out = map[string]any{"check_runs": checks}
	default:
		f.t.Errorf("unexpected provider fixture request: %s %s", r.Method, r.URL.Path)
		status, out = http.StatusNotFound, map[string]string{"message": "unprogrammed fixture request"}
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(out); err != nil {
		f.t.Error("encode fixture response")
	}
}

func callCP(t *testing.T, endpoint cpEndpoint, method string, body map[string]any, authenticated bool) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint.URL+"/controlplane.v1.GitCredentialInternalService/"+method, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if authenticated {
		req.Header.Set("X-Private-Fixture-Caller", "internal")
		req.Header.Set("X-Private-Fixture-Secret", endpoint.secret)
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("CP fixture request failed: %T", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		t.Fatal("CP fixture response read failed")
	}
	for _, forbidden := range []string{"ghp_futurechat", "ghs_futurechat", "PRIVATE KEY", "access_token", "accessToken", endpoint.secret} {
		if forbidden != "" && bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("CP returned credential-shaped fixture data")
		}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal("CP returned malformed JSON")
	}
	return resp.StatusCode, out
}

func TestFutureChatBCPRealSQLBoundary(t *testing.T) {
	requirePrivatePipeline(t)
	provider := &checkProvider{t: t}
	github := newGitHubBoundary(t, provider)
	endpoint := startCP(t, github)
	body := map[string]any{
		"userId": endpoint.OwnerID, "repositoryId": "42", "owner": "acme", "repo": "example",
		"headSha": strings.Repeat("a", 40), "name": "ReliantReview", "externalId": "fixture/assignment/1",
		"runUrl": "https://app.reliant.test/runs/reviewer", "status": "REVIEW_CHECK_STATUS_COMPLETED",
		"conclusion": "REVIEW_CHECK_CONCLUSION_SUCCESS", "summary": "private boundary fixture; not upstream review authority",
	}
	status, _ := callCP(t, endpoint, "PublishReviewCheck", body, false)
	if (status != http.StatusForbidden && status != http.StatusUnauthorized) || len(github.Requests()) != 0 {
		t.Fatalf("untrusted caller gate: HTTP%d provider requests=%d", status, len(github.Requests()))
	}
	status, out := callCP(t, endpoint, "PublishReviewCheck", body, true)
	if status != http.StatusOK || out["checkId"] != "99" {
		t.Fatalf("actual SQL-backed CP publication failed: HTTP%d code=%v", status, out["code"])
	}
	status, _ = callCP(t, endpoint, "PublishReviewCheck", body, true)
	if status < 400 {
		t.Fatal("duplicate create was unexpectedly authorized")
	}
	binding := map[string]any{}
	for _, key := range []string{"userId", "repositoryId", "owner", "repo", "headSha", "name", "externalId", "runUrl"} {
		binding[key] = body[key]
	}
	status, out = callCP(t, endpoint, "ReconcileReviewCheck", binding, true)
	if status != http.StatusOK || out["checkId"] != "99" || fmt.Sprint(out["state"]) != "REVIEW_CHECK_RECONCILE_STATE_FOUND" {
		t.Fatalf("actual CP reconciliation failed: HTTP%d state=%v", status, out["state"])
	}
	provider.mu.Lock()
	writes := provider.writes
	provider.mu.Unlock()
	if writes != 1 {
		t.Fatalf("duplicate provider check creation attempts: %d", writes)
	}
}
