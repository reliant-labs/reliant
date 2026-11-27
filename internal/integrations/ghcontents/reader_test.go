// Copyright (c) 2026 Reliant Labs

package ghcontents

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

type fakeTokens struct {
	mu   sync.Mutex
	reqs []gitcredentialclient.InstallationRequest
	err  error
}

func (f *fakeTokens) MintInstallationToken(_ context.Context, req gitcredentialclient.InstallationRequest) (gitcredentialclient.InstallationToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return gitcredentialclient.InstallationToken{}, f.err
	}
	return gitcredentialclient.InstallationToken{Token: gitcredentialclient.NewToken("inst-"+req.ExternalUserID, nil)}, nil
}

const sha = "3b18e512dba79e4c8300dd08aeb37f8e728b8dad"

func serve(t *testing.T, tokens *fakeTokens, maxBytes int64, handler http.HandlerFunc) (Service, *[]*http.Request) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []*http.Request
	)
	srv := netguardtest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r)
		mu.Unlock()
		handler(w, r)
	}))
	return New(Deps{Tokens: tokens, APIBaseURL: srv.URL, HTTPClient: srv.Client(), MaxBytes: maxBytes}), &reqs
}

func request() Request {
	return Request{UserID: "alice", OrgID: "org-1", Owner: "acme", Repo: "shop", Path: ".reliant/pipeline.yaml", Ref: sha}
}

func TestRead_ReturnsTheRawFileAsTheApp(t *testing.T) {
	tokens := &fakeTokens{}
	r, reqs := serve(t, tokens, 0, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("version: 1\n")) })

	got, err := r.Read(context.Background(), request())
	require.NoError(t, err)
	assert.Equal(t, "version: 1\n", string(got))
	require.Len(t, *reqs, 1)
	req := (*reqs)[0]
	assert.Equal(t, "/repos/acme/shop/contents/.reliant/pipeline.yaml", req.URL.Path)
	assert.Equal(t, sha, req.URL.Query().Get("ref"))
	assert.Equal(t, "application/vnd.github.raw", req.Header.Get("Accept"))
	assert.Equal(t, "Bearer inst-alice", req.Header.Get("Authorization"))
	require.Len(t, tokens.reqs, 1)
	assert.Equal(t, gitcredentialclient.InstallationRequest{
		ExternalUserID: "alice", Owner: "acme", Repo: "shop", OrgID: "org-1",
		Permissions: map[string]string{"contents": "read"},
	}, tokens.reqs[0], "a read-only token scoped to the one repository")
}

func TestRead_MapsGitHubsAnswers(t *testing.T) {
	r, _ := serve(t, &fakeTokens{}, 0, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	_, err := r.Read(context.Background(), request())
	assert.ErrorIs(t, err, ErrNotFound)

	r, _ = serve(t, &fakeTokens{}, 4, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("too long")) })
	_, err = r.Read(context.Background(), request())
	assert.ErrorIs(t, err, ErrTooLarge)

	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway} {
		r, _ := serve(t, &fakeTokens{}, 0, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		_, err := r.Read(context.Background(), request())
		require.Error(t, err)
		for _, known := range []error{ErrNotFound, ErrTooLarge, ErrInvalid} {
			assert.False(t, errors.Is(err, known), "%d is not %v", status, known)
		}
	}
}

func TestRead_RefusesBeforeSending(t *testing.T) {
	tokens := &fakeTokens{}
	r, reqs := serve(t, tokens, 0, func(w http.ResponseWriter, _ *http.Request) {})
	for _, mutate := range []func(*Request){
		func(q *Request) { q.Owner = "../x" },
		func(q *Request) { q.Path = "/abs" },
		func(q *Request) { q.Path = "a/../b" },
		func(q *Request) { q.Ref = "a..b" },
		func(q *Request) { q.Ref = "" },
	} {
		q := request()
		mutate(&q)
		_, err := r.Read(context.Background(), q)
		assert.ErrorIs(t, err, ErrInvalid)
	}
	assert.Empty(t, *reqs)
	assert.Empty(t, tokens.reqs, "no token is minted for a request that cannot be sent")

	mintErr := errors.New("not connected")
	r, reqs = serve(t, &fakeTokens{err: mintErr}, 0, func(w http.ResponseWriter, _ *http.Request) {})
	_, err := r.Read(context.Background(), request())
	assert.ErrorIs(t, err, mintErr)
	assert.Empty(t, *reqs)
}
