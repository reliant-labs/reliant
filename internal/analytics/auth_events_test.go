// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackAuthSignedInIsForgesStandardEvent(t *testing.T) {
	c, buf := jsonClient(t, consent{"u-1": true})
	c.ForUser(context.Background(), "u-1").TrackAuthSignedIn()

	recs := records(t, buf)
	require.Len(t, recs, 1)
	rec := recs[0]
	assert.Equal(t, "product.auth_signed_in", rec["event.name"])
	assert.Equal(t, "u-1", rec["user.id"])
	assert.Equal(t, "resumed", rec["product.kind"])
	assert.Equal(t, AuthProvider, rec["product.provider"])
	assert.Equal(t, "backend", rec["app.client"])
}

func TestTrackAuthSignedInHonoursTheOptOut(t *testing.T) {
	c, buf := jsonClient(t, consent{"opted-out": false})
	ctx := context.Background()
	opted := c.ForUser(ctx, "opted-out")
	assert.False(t, opted.Recording())
	opted.TrackAuthSignedIn()
	// A consent read that fails records nothing either.
	unknown := c.ForUser(ctx, "unknown-user")
	assert.False(t, unknown.Recording())
	unknown.TrackAuthSignedIn()
	// The zero Tracker (no client installed) is silent.
	Tracker{}.TrackAuthSignedIn()

	assert.Empty(t, buf.String())
	assert.True(t, c.ForUser(ctx, "").Recording(), "an event that names nobody needs no consent")
}
