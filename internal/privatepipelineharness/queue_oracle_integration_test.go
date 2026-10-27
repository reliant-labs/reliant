//go:build integration

package privatepipelineharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const allowedSubjectQuery = `query ReliantQueueSubject($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    id databaseId nameWithOwner defaultBranchRef { name }
    pullRequest(number: $number) {
      id databaseId url number state isDraft headRefOid baseRefName isMergeQueueEnabled
      repository { id databaseId }
      headRepository { id databaseId }
      mergeQueueEntry { id position pullRequest { id headRefOid repository { id } } }
    }
  }
}`
const allowedEnqueueMutation = `mutation ReliantEnqueue($input: EnqueuePullRequestInput!) {
  enqueuePullRequest(input: $input) {
    mergeQueueEntry { id position pullRequest { id headRefOid repository { id } } }
  }
}`

func validateQueueOperation(auth, query string, variables map[string]json.RawMessage, expectedHead string) error {
	denied := fmt.Errorf("provider fixture refused unapproved GraphQL operation")
	if auth != "Bearer ghp_futurechat_b_fixture_owner" {
		return denied
	}
	switch query {
	case allowedSubjectQuery:
		if len(variables) != 3 {
			return denied
		}
		var owner, repo string
		var number int
		if json.Unmarshal(variables["owner"], &owner) != nil || json.Unmarshal(variables["repo"], &repo) != nil || json.Unmarshal(variables["number"], &number) != nil || owner != "acme" || repo != "example" || number != 7 {
			return denied
		}
	case allowedEnqueueMutation:
		if len(variables) != 1 {
			return denied
		}
		var input struct {
			PullRequestID string `json:"pullRequestId"`
			ExpectedHead  string `json:"expectedHeadOid"`
		}
		decoder := json.NewDecoder(bytes.NewReader(variables["input"]))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || input.PullRequestID != "pr-7" || input.ExpectedHead != expectedHead {
			return denied
		}
	default:
		return denied
	}
	return nil
}

func TestQueueOracleRejectsForbiddenOperations(t *testing.T) {
	valid := map[string]any{"input": map[string]any{"pullRequestId": "pr-7", "expectedHeadOid": strings.Repeat("a", 40)}}
	for _, tc := range []struct {
		name, query, auth string
		variables         map[string]any
	}{
		{"jump", allowedEnqueueMutation, "Bearer ghp_futurechat_b_fixture_owner", map[string]any{"input": map[string]any{"pullRequestId": "pr-7", "expectedHeadOid": strings.Repeat("a", 40), "jump": true}}},
		{"different mutation", "mutation ReliantEnqueue { mergePullRequest(input:{pullRequestId:\"pr-7\"}) { clientMutationId } }", "Bearer ghp_futurechat_b_fixture_owner", valid},
		{"extra mutation field", strings.Replace(allowedEnqueueMutation, "enqueuePullRequest(input: $input)", "enqueuePullRequest(input: $input, jump: true)", 1), "Bearer ghp_futurechat_b_fixture_owner", valid},
		{"wrong read variables", allowedSubjectQuery, "Bearer ghp_futurechat_b_fixture_owner", map[string]any{"owner": "other", "repo": "example", "number": 7}},
		{"no auth", allowedEnqueueMutation, "", valid},
		{"unknown query", "query { viewer { login } }", "Bearer ghp_futurechat_b_fixture_owner", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &checkProvider{t: t, check: map[string]any{"conclusion": "success"}}
			body, _ := json.Marshal(map[string]any{"query": tc.query, "variables": tc.variables})
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/graphql", bytes.NewReader(body))
			req.Header.Set("Authorization", tc.auth)
			response := httptest.NewRecorder()
			provider.ServeHTTP(response, req)
			if response.Code != http.StatusBadRequest || provider.queueWrites != 0 || provider.queued {
				t.Fatal("forbidden GraphQL reached successful queue oracle")
			}
		})
	}
}
