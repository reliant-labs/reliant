// Copyright (c) 2025 Reliant Labs
package claimcheck_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	commonpb "go.temporal.io/api/common/v1"
	"google.golang.org/protobuf/proto"
)

// These run against a real, freshly migrated Postgres database, so they also
// exercise the temporal_payload_blobs migration.

func TestPostgresStorePutGet(t *testing.T) {
	_, sqlDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	store := claimcheck.NewPostgresStore(sqlDB)

	if err := store.Put(ctx, "sha256:aa", []byte("one"), 3); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Re-put is an upsert (content-addressed), not a conflict error.
	if err := store.Put(ctx, "sha256:aa", []byte("one"), 3); err != nil {
		t.Fatalf("re-Put: %v", err)
	}
	got, err := store.Get(ctx, "sha256:aa")
	if err != nil || string(got) != "one" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if _, err := store.Get(ctx, "sha256:missing"); !errors.Is(err, claimcheck.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
}

func TestPostgresStoreCodecRoundTrip(t *testing.T) {
	_, sqlDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	store := claimcheck.NewPostgresStore(sqlDB)

	large := &commonpb.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     make([]byte, 100<<10),
	}
	encoded, err := claimcheck.NewCodec(store).Encode([]*commonpb.Payload{large})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := claimcheck.NewCodec(store).Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !proto.Equal(decoded[0], large) {
		t.Fatal("round trip through Postgres changed the payload")
	}
	var sizeBytes, dataLen int
	if err := sqlDB.QueryRow(`SELECT size_bytes, length(data) FROM temporal_payload_blobs`).Scan(&sizeBytes, &dataLen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if sizeBytes != proto.Size(large) || dataLen >= sizeBytes {
		t.Errorf("size_bytes=%d data=%d; want raw size recorded and data compressed", sizeBytes, dataLen)
	}
}

func TestPostgresStoreGC(t *testing.T) {
	_, sqlDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	store := claimcheck.NewPostgresStore(sqlDB)

	// 2500 stale rows spans three delete batches; 3 fresh rows must survive.
	if _, err := sqlDB.Exec(`
		INSERT INTO temporal_payload_blobs (key, data, size_bytes, last_referenced_at)
		SELECT 'sha256:old' || g, '\x00'::bytea, 1, NOW() - interval '40 days'
		FROM generate_series(1, 2500) g`); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	for _, k := range []string{"sha256:new1", "sha256:new2"} {
		if err := store.Put(ctx, k, []byte{1}, 1); err != nil {
			t.Fatal(err)
		}
	}
	// A stale row that gets re-put is refreshed and must survive GC.
	if _, err := sqlDB.Exec(`INSERT INTO temporal_payload_blobs (key, data, size_bytes, last_referenced_at)
		VALUES ('sha256:revived', '\x00'::bytea, 1, NOW() - interval '40 days')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "sha256:revived", []byte{0}, 1); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteExpired(ctx, claimcheck.DefaultGCHorizon)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 2500 {
		t.Errorf("deleted = %d, want 2500", deleted)
	}
	var remaining int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM temporal_payload_blobs`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 3 {
		t.Errorf("remaining = %d, want 3", remaining)
	}
}

func TestGCHorizonFromEnv(t *testing.T) {
	t.Setenv(claimcheck.GCHorizonEnv, "")
	if got := claimcheck.GCHorizonFromEnv(); got != claimcheck.DefaultGCHorizon {
		t.Errorf("unset = %v", got)
	}
	t.Setenv(claimcheck.GCHorizonEnv, "48h")
	if got := claimcheck.GCHorizonFromEnv(); got != 48*time.Hour {
		t.Errorf("48h = %v", got)
	}
	t.Setenv(claimcheck.GCHorizonEnv, "garbage")
	if got := claimcheck.GCHorizonFromEnv(); got != claimcheck.DefaultGCHorizon {
		t.Errorf("garbage = %v", got)
	}
}
