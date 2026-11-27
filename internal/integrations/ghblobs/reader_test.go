// Copyright (c) 2026 Reliant Labs

package ghblobs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

type tokenFunc func(ctx context.Context, userID string) (string, error)

func (f tokenFunc) Token(ctx context.Context, userID string) (string, error) { return f(ctx, userID) }

func ownToken(_ context.Context, userID string) (string, error) { return "tok-" + userID, nil }

const blob = "3b18e512dba79e4c8300dd08aeb37f8e728b8dad"

// fakeGitHub answers the blobs endpoint with handler, recording each request.
type fakeGitHub struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (g *fakeGitHub) serve(t *testing.T, maxBytes int64, handler http.HandlerFunc) Service {
	t.Helper()
	srv := netguardtest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.reqs = append(g.reqs, r)
		g.mu.Unlock()
		handler(w, r)
	}))
	return New(Deps{Tokens: tokenFunc(ownToken), APIBaseURL: srv.URL, HTTPClient: srv.Client(), MaxBytes: maxBytes})
}

func TestRead_ReturnsTheRawBlobAsTheCaller(t *testing.T) {
	g := &fakeGitHub{}
	r := g.serve(t, 0, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("package main\n")) })

	got, err := r.Read(context.Background(), "alice", "acme", "shop", strings.ToUpper(blob))
	require.NoError(t, err)
	assert.Equal(t, "package main\n", string(got))
	require.Len(t, g.reqs, 1)
	req := g.reqs[0]
	assert.Equal(t, "/repos/acme/shop/git/blobs/"+blob, req.URL.Path, "the id is lower-cased")
	assert.Equal(t, "application/vnd.github.raw", req.Header.Get("Accept"))
	assert.Equal(t, "Bearer tok-alice", req.Header.Get("Authorization"), "the viewer's own token")
}

func TestRead_MapsGitHubsAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		want   error
	}{
		"401: reconnect":      {http.StatusUnauthorized, ErrTokenRejected},
		"404: gone or hidden": {http.StatusNotFound, ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			r := (&fakeGitHub{}).serve(t, 0, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status) })
			_, err := r.Read(context.Background(), "alice", "acme", "shop", blob)
			assert.ErrorIs(t, err, tc.want)
		})
	}
	// A rate limit or GitHub failing is neither: the read may be retried.
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway} {
		r := (&fakeGitHub{}).serve(t, 0, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		_, err := r.Read(context.Background(), "alice", "acme", "shop", blob)
		require.Error(t, err)
		for _, known := range []error{ErrTokenRejected, ErrNotFound, ErrTooLarge, ErrInvalid} {
			assert.False(t, errors.Is(err, known), "%d is not %v", status, known)
		}
	}
}

func TestRead_RefusesABlobOverTheBound(t *testing.T) {
	big := strings.Repeat("x", 11)
	t.Run("declared length", func(t *testing.T) {
		r := (&fakeGitHub{}).serve(t, 10, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(big)) })
		_, err := r.Read(context.Background(), "alice", "acme", "shop", blob)
		assert.ErrorIs(t, err, ErrTooLarge)
	})
	t.Run("chunked", func(t *testing.T) {
		r := (&fakeGitHub{}).serve(t, 10, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(big[:5]))
			w.(http.Flusher).Flush() // no Content-Length: the body is counted
			_, _ = w.Write([]byte(big[5:]))
		})
		_, err := r.Read(context.Background(), "alice", "acme", "shop", blob)
		assert.ErrorIs(t, err, ErrTooLarge)
	})
	t.Run("exactly the bound", func(t *testing.T) {
		r := (&fakeGitHub{}).serve(t, 10, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(big[:10])) })
		got, err := r.Read(context.Background(), "alice", "acme", "shop", blob)
		require.NoError(t, err)
		assert.Len(t, got, 10)
	})
}

func TestRead_SendsNothingItCannotName(t *testing.T) {
	g := &fakeGitHub{}
	r := g.serve(t, 0, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
	for _, in := range [][3]string{
		{"acme", "shop", "not-a-sha"},
		{"acme", "shop", blob + "00"},
		{"ac/me", "shop", blob},
		{"acme", "../x", blob + "/"},
		{"", "shop", blob},
	} {
		_, err := r.Read(context.Background(), "alice", in[0], in[1], in[2])
		assert.ErrorIs(t, err, ErrInvalid, "%v", in)
	}
	assert.Empty(t, g.reqs)
}

func TestRead_PassesTheTokenSourcesRefusalThrough(t *testing.T) {
	notConnected := errors.New("not connected")
	g := &fakeGitHub{}
	srvReader := g.serve(t, 0, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }).(*Reader)
	srvReader.tokens = tokenFunc(func(context.Context, string) (string, error) { return "", notConnected })
	_, err := srvReader.Read(context.Background(), "alice", "acme", "shop", blob)
	assert.ErrorIs(t, err, notConnected)
	assert.Empty(t, g.reqs)
}

// Reads past MaxInFlight wait for a slot, so the bytes a Reader holds stay
// bounded however many viewers ask at once.
func TestRead_HoldsAtMostMaxInFlightReads(t *testing.T) {
	var (
		mu        sync.Mutex
		now, peak int
		release   = make(chan struct{})
		arrived   = make(chan struct{}, 2*MaxInFlight)
	)
	g := &fakeGitHub{}
	r := g.serve(t, 0, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		now++
		peak = max(peak, now)
		mu.Unlock()
		arrived <- struct{}{}
		<-release
		mu.Lock()
		now--
		mu.Unlock()
		_, _ = w.Write([]byte("x"))
	})
	var wg sync.WaitGroup
	for range 2 * MaxInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Read(context.Background(), "alice", "acme", "shop", blob)
			assert.NoError(t, err)
		}()
	}
	for range MaxInFlight {
		<-arrived
	}
	select {
	case <-arrived:
		t.Fatal("a read past MaxInFlight reached GitHub before a slot freed")
	default:
	}
	close(release)
	wg.Wait()
	assert.Equal(t, MaxInFlight, peak)
}

func TestRead_WaitingForASlotEndsWithTheContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	g := &fakeGitHub{}
	r := g.serve(t, 0, func(w http.ResponseWriter, _ *http.Request) { <-release })
	for range MaxInFlight {
		go func() { _, _ = r.Read(context.Background(), "alice", "acme", "shop", blob) }()
	}
	require.Eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.reqs) == MaxInFlight }, 5*time.Second, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Read(ctx, "alice", "acme", "shop", blob)
	assert.ErrorIs(t, err, context.Canceled)
}
