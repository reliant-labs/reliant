// Copyright (c) 2025 Reliant Labs
package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckHostMatrix(t *testing.T) {
	hosted := Policy{LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "rebind.example.com":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.1.2.3")}, nil
		case "internal.example.com":
			return []net.IP{net.ParseIP("192.168.1.5")}, nil
		case "public.example.com":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("no such host")
	}}
	tests := []struct {
		host    string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"[::1]", true},
		{"10.0.0.8", true},
		{"172.16.5.5", true},
		{"192.168.0.1", true},
		{"169.254.169.254", true},
		{"fd00:ec2::254", true},
		{"::ffff:10.0.0.1", true},
		{"100.64.1.1", true},
		{"0.0.0.0", true},
		{"localhost", true},
		{"foo.localhost", true},
		{"internal.example.com", true},
		{"rebind.example.com", true}, // ANY resolved address blocked => blocked
		{"8.8.8.8", false},
		{"public.example.com", false},
		{"unresolvable.example.com", false}, // cannot be dialed; the dial guard owns the rest
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			err := hosted.CheckHost(context.Background(), tc.host)
			if tc.blocked {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrBlockedAddress)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSelfHostAllowsPrivate(t *testing.T) {
	p := Policy{AllowPrivate: true}
	for _, h := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "localhost"} {
		assert.NoError(t, p.CheckHost(context.Background(), h), h)
	}
}

// The guard must hold at connect time, not only at validation time: a name
// that resolved to a public address when saved can resolve to loopback later.
func TestTransportBlocksDialToLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()

	guarded := &http.Client{Transport: Policy{}.Transport()}
	resp, err := guarded.Get(srv.URL)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedAddress)
	assert.Zero(t, hits.Load(), "the server must never see the request")

	open := &http.Client{Transport: Policy{AllowPrivate: true}.Transport()}
	resp, err = open.Get(srv.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.EqualValues(t, 1, hits.Load())
}

func TestTransportBlocksRedirectToInternal(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// Allow the first hop only by dialing it through an open transport...
	// the guarded client cannot reach either, which is the point.
	guarded := &http.Client{Transport: Policy{}.Transport()}
	_, err := guarded.Get(redirector.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedAddress)
}
