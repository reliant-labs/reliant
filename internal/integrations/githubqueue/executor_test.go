package githubqueue

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testToken = "ghs_private_queue_token"

type credential struct{}

func (credential) Apply(req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+testToken)
	return nil
}
func (credential) ConnectionID() string      { return "github-app" }
func (credential) Scrub(s string) string     { return strings.ReplaceAll(s, testToken, "[redacted]") }
func (credential) Params() map[string]string { return nil }

type fake struct {
	t                    *testing.T
	call                 httpaction.ExecutorCall
	repo                 map[string]any
	pr                   map[string]any
	reads, mutations     int
	mutationStatus       int
	mutationBody         any
	readStatus           int
	readErrors           any
	readNull             bool
	commitOnFailure      bool
	dropMutation         bool
	changeHeadOnMutation bool
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{t: t}
	f.pr = map[string]any{"id": "PR_42", "number": 42, "state": "OPEN", "isDraft": false, "headRefOid": testSHA,
		"baseRefName": "main", "isMergeQueueEnabled": true,
		"repository":     map[string]any{"id": "R_101", "databaseId": 101},
		"headRepository": map[string]any{"id": "R_101", "databaseId": 101}, "mergeQueueEntry": nil}
	f.repo = map[string]any{"id": "R_101", "databaseId": 101, "nameWithOwner": "acme/widgets",
		"defaultBranchRef": map[string]any{"name": "main"}, "pullRequest": f.pr}
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(f.serve))
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	f.call = httpaction.ExecutorCall{Runner: httpaction.NewRunner(netguardtest.Guard()).WithRootCAs(pool), Credential: credential{},
		Manifest: &reliantv1.IntegrationManifest{Connection: &reliantv1.ConnectionSpec{BaseUrl: srv.URL}},
		Params:   map[string]any{"owner": "acme", "repo": "widgets", "repository_id": float64(101), "pull_number": float64(42), "expectedHeadOid": testSHA}}
	return f
}

func queueEntry() map[string]any {
	return map[string]any{"id": "MQE_1", "position": 2, "pullRequest": map[string]any{
		"id": "PR_42", "headRefOid": testSHA, "repository": map[string]any{"id": "R_101"}}}
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	require.Equal(f.t, "POST", r.Method)
	require.Equal(f.t, "/graphql", r.URL.Path)
	require.Equal(f.t, "Bearer "+testToken, r.Header.Get("Authorization"))
	var body struct {
		Query     string
		Variables map[string]any
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))
	w.Header().Set("Content-Type", "application/json")
	if body.Query == subjectQuery {
		f.reads++
		require.Equal(f.t, map[string]any{"owner": "acme", "repo": "widgets", "number": float64(42)}, body.Variables)
		if f.readStatus != 0 {
			w.WriteHeader(f.readStatus)
			fmt.Fprint(w, testToken)
			return
		}
		data := any(map[string]any{"repository": f.repo})
		if f.readNull {
			data = nil
		}
		out := map[string]any{"data": data}
		if f.readErrors != nil {
			out["errors"] = f.readErrors
		}
		require.NoError(f.t, json.NewEncoder(w).Encode(out))
		return
	}
	require.Equal(f.t, enqueueMutation, body.Query)
	require.Equal(f.t, map[string]any{"input": map[string]any{"pullRequestId": "PR_42", "expectedHeadOid": testSHA}}, body.Variables)
	require.NotContains(f.t, body.Query, "mergePullRequest")
	require.NotContains(f.t, body.Query, "jump")
	f.mutations++
	if f.changeHeadOnMutation {
		f.pr["headRefOid"] = strings.Repeat("b", 40)
	}
	if f.commitOnFailure {
		f.pr["mergeQueueEntry"] = queueEntry()
	}
	if f.dropMutation {
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(f.t, err)
		require.NoError(f.t, conn.Close())
		return
	}
	if f.mutationStatus != 0 {
		w.WriteHeader(f.mutationStatus)
		fmt.Fprint(w, testToken)
		return
	}
	if f.mutationBody != nil {
		require.NoError(f.t, json.NewEncoder(w).Encode(f.mutationBody))
		return
	}
	f.pr["mergeQueueEntry"] = queueEntry()
	require.NoError(f.t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"enqueuePullRequest": map[string]any{"mergeQueueEntry": queueEntry()}}}))
}

func invoke(t *testing.T, f *fake) *httpaction.Result {
	t.Helper()
	res, err := enqueue(context.Background(), f.call)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotContains(t, fmt.Sprintf("%+v", res), testToken)
	return res
}

func TestEnqueueExactMutation(t *testing.T) {
	f := newFake(t)
	res := invoke(t, f)
	require.False(t, res.IsError)
	require.Equal(t, "accepted", res.Data["outcome"])
	require.Equal(t, "MQE_1", res.Data["queue_entry_id"])
	require.Equal(t, 2, res.Data["position"])
	require.Equal(t, 1, f.reads)
	require.Equal(t, 1, f.mutations)
	res = invoke(t, f)
	require.False(t, res.IsError)
	require.Equal(t, true, res.Data["already_queued"])
	require.Equal(t, 1, f.mutations, "retry must read membership before mutation")
}

func TestRejectSubjectConfusion(t *testing.T) {
	cases := map[string]func(*fake){
		"wrong repository ID":    func(f *fake) { f.repo["databaseId"] = 102 },
		"wrong repository name":  func(f *fake) { f.repo["nameWithOwner"] = "other/widgets" },
		"cross repo PR":          func(f *fake) { f.pr["repository"] = map[string]any{"id": "R_102", "databaseId": 102} },
		"wrong PR":               func(f *fake) { f.pr["number"] = 43 },
		"fork":                   func(f *fake) { f.pr["headRepository"] = map[string]any{"id": "R_FORK", "databaseId": 202} },
		"unknown fork":           func(f *fake) { f.pr["headRepository"] = nil },
		"missing PR":             func(f *fake) { f.repo["pullRequest"] = nil },
		"closed":                 func(f *fake) { f.pr["state"] = "CLOSED" },
		"draft":                  func(f *fake) { f.pr["isDraft"] = true },
		"missing draft fact":     func(f *fake) { delete(f.pr, "isDraft") },
		"not default target":     func(f *fake) { f.pr["baseRefName"] = "release" },
		"unknown default target": func(f *fake) { f.repo["defaultBranchRef"] = nil },
		"queue disabled":         func(f *fake) { f.pr["isMergeQueueEnabled"] = false },
		"auto flag not queue": func(f *fake) {
			f.pr["isMergeQueueEnabled"] = false
			f.pr["autoMergeRequest"] = map[string]any{"enabledAt": "now"}
		},
		"wrong queue entry subject": func(f *fake) {
			e := queueEntry()
			e["pullRequest"] = map[string]any{"id": "PR_OTHER"}
			f.pr["mergeQueueEntry"] = e
		},
		"ambiguous partial data": func(f *fake) { f.readErrors = []any{map[string]any{"type": "FORBIDDEN", "message": testToken}} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			change(f)
			res := invoke(t, f)
			require.True(t, res.IsError)
			require.Equal(t, 0, f.mutations)
		})
	}
}

func TestStaleHead(t *testing.T) {
	f := newFake(t)
	f.pr["headRefOid"] = strings.Repeat("b", 40)
	res := invoke(t, f)
	require.Equal(t, "stale", res.Data["outcome"])
	require.False(t, res.Retryable)
	require.Equal(t, 0, f.mutations)
}

func TestMutationFailureReconciliation(t *testing.T) {
	for _, commit := range []bool{true, false} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			f := newFake(t)
			f.mutationStatus = 504
			f.commitOnFailure = commit
			res := invoke(t, f)
			require.Equal(t, 2, f.reads)
			require.Equal(t, 1, f.mutations)
			if commit {
				require.False(t, res.IsError)
				require.Equal(t, true, res.Data["already_queued"])
			} else {
				require.True(t, res.IsError)
				require.True(t, res.Retryable)
				require.Equal(t, "unavailable", res.Data["outcome"])
			}
		})
	}
}

func TestMutationRaceReportsStaleWithoutReplay(t *testing.T) {
	f := newFake(t)
	f.changeHeadOnMutation = true
	f.mutationBody = map[string]any{"data": nil, "errors": []any{map[string]any{"type": "UNPROCESSABLE", "message": testToken}}}
	res := invoke(t, f)
	require.Equal(t, "stale", res.Data["outcome"])
	require.Equal(t, 1, f.mutations)
	require.Equal(t, 2, f.reads)
}

func TestTransportUnknownReadsCommittedMembership(t *testing.T) {
	f := newFake(t)
	f.dropMutation = true
	f.commitOnFailure = true
	res := invoke(t, f)
	require.False(t, res.IsError)
	require.Equal(t, 1, f.mutations)
	require.Equal(t, 2, f.reads)
}

func TestGraphQLFailuresRedacted(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			f := newFake(t)
			data := any(nil)
			if partial {
				data = map[string]any{"enqueuePullRequest": map[string]any{"mergeQueueEntry": queueEntry()}}
			}
			f.mutationBody = map[string]any{"data": data, "errors": []any{map[string]any{"type": "FORBIDDEN", "message": testToken}}}
			res := invoke(t, f)
			require.True(t, res.IsError)
			require.Equal(t, 2, f.reads)
			require.Equal(t, 1, f.mutations)
		})
	}
	f := newFake(t)
	f.readStatus = 403
	res := invoke(t, f)
	require.Equal(t, "failed", res.Data["outcome"])
	require.False(t, res.Retryable)
}

func TestInvalidParamsNoNetwork(t *testing.T) {
	for key, value := range map[string]any{"owner": "../acme", "repo": "..", "repository_id": -1, "pull_number": 1.5, "expectedHeadOid": "main"} {
		t.Run(key, func(t *testing.T) {
			f := newFake(t)
			f.call.Params[key] = value
			res := invoke(t, f)
			require.Equal(t, "invalid", res.Data["outcome"])
			require.Zero(t, f.reads)
			require.Zero(t, f.mutations)
		})
	}
}

func TestCatalogRegistration(t *testing.T) {
	// Embedded catalog inputs are in this test binary; no child reads repo files.
	ms := catalog.MustBuiltin().Manifests()
	var action *reliantv1.ActionSpec
	for _, m := range ms {
		if m.GetId() == "github" {
			for _, a := range m.GetActions() {
				if a.GetId() == "pr.enqueue" {
					action = a
				}
			}
		}
	}
	require.NotNil(t, action)
	require.Equal(t, "go:"+Executor, action.GetExecutor())
	// Check only our executor, not unrelated packages absent from this binary.
	require.NoError(t, httpaction.Executors().CheckManifests([]*reliantv1.IntegrationManifest{{Actions: []*reliantv1.ActionSpec{proto.Clone(action).(*reliantv1.ActionSpec)}}}))
}
