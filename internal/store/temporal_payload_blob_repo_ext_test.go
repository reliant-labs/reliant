// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestTemporalPayloadBlobsInterleaveWithTheHandPath: blobs the previous
// release's hand SQL wrote are read, refreshed and collected by the port, and
// the other way round.
func TestTemporalPayloadBlobsInterleaveWithTheHandPath(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	// The previous release's Put, and a stale row it left 40 days ago.
	mustExec(t, env, `INSERT INTO temporal_payload_blobs (key, data, size_bytes) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET last_referenced_at = NOW()`, "sha256:hand", []byte("hand"), 4)
	mustExec(t, env, `INSERT INTO temporal_payload_blobs (key, data, size_bytes, last_referenced_at)
		VALUES ('sha256:stale', '\x00'::bytea, 1, NOW() - interval '40 days'),
		       ('sha256:revived', '\x00'::bytea, 1, NOW() - interval '40 days')`)

	if err := store.PutTemporalPayloadBlob(ctx, env.ORM, "sha256:orm", []byte("orm\x00bytes"), 9); err != nil {
		t.Fatal(err)
	}
	// Content-addressed: a re-put is an upsert that refreshes the stamp only.
	if err := store.PutTemporalPayloadBlob(ctx, env.ORM, "sha256:revived", []byte("ignored"), 99); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{"sha256:hand": "hand", "sha256:orm": "orm\x00bytes", "sha256:revived": "\x00"} {
		got, err := store.TemporalPayloadBlobData(ctx, env.ORM, key)
		if err != nil || string(got) != want {
			t.Fatalf("TemporalPayloadBlobData(%s) = %q, %v; want %q", key, got, err, want)
		}
		var hand []byte
		if err := env.SQL.QueryRow(`SELECT data FROM temporal_payload_blobs WHERE key = $1`, key).Scan(&hand); err != nil || string(hand) != want {
			t.Fatalf("hand read of %s = %q, %v", key, hand, err)
		}
	}
	var size int
	if err := env.SQL.QueryRow(`SELECT size_bytes FROM temporal_payload_blobs WHERE key = 'sha256:revived'`).Scan(&size); err != nil || size != 1 {
		t.Fatalf("re-put changed size_bytes to %d (%v); the conflict must only refresh the stamp", size, err)
	}
	if _, err := store.TemporalPayloadBlobData(ctx, env.ORM, "sha256:missing"); !svcerr.IsNotFound(err) {
		t.Fatalf("missing blob = %v, want svcerr.NotFound", err)
	}

	// One batch of one: only the stale row goes, and a short batch says so.
	horizon := 30 * 24 * time.Hour
	if n, err := store.DeleteExpiredTemporalPayloadBlobs(ctx, env.ORM, horizon, 1); err != nil || n != 1 {
		t.Fatalf("first batch deleted %d, %v; want 1", n, err)
	}
	if n, err := store.DeleteExpiredTemporalPayloadBlobs(ctx, env.ORM, horizon, 1); err != nil || n != 0 {
		t.Fatalf("second batch deleted %d, %v; want 0", n, err)
	}
	var left int
	if err := env.SQL.QueryRow(`SELECT count(*) FROM temporal_payload_blobs`).Scan(&left); err != nil || left != 3 {
		t.Fatalf("%d blobs left, %v; want 3", left, err)
	}
}
