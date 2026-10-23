package crud

import (
	"bytes"
	"context"
	"testing"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
)

// A nil Go slice binds as SQL NULL, so any NOT NULL slice column left unset
// fails the INSERT outright. The Repo has always normalized nil ARRAY
// columns for that reason — but it selected them by the `,array` struct tag,
// and a `bytes` column is a slice field WITHOUT that tag.
//
// Nothing noticed, because `bytes` was a shape the generator could not
// produce: forge projected the proto kind to Go `string`, so the CRUD
// conversion gate refused the pairing and no BYTEA column ever reached a
// generated entity struct. The moment the projection was corrected to
// []byte — protoc-gen-go's own type — a NOT NULL BYTEA column would have
// failed every Create where the caller did not set the field, which is the
// default for an optional-in-practice blob.
//
// The selector is now NOT NULL + slice-typed, which covers both and states
// the actual reason. A NULLABLE slice column is deliberately excluded: there
// nil IS the absent value, and normalizing it would erase the difference
// between "no value" and "empty value" the column was declared to keep.

// blobRow mixes the shapes: a NOT NULL bytea, a NOT NULL array, and a
// NULLABLE bytea that must keep its nil.
type blobRow struct {
	bun.BaseModel `bun:"table:blob_rows,alias:blob_rows"`

	ID       string   `bun:"id,pk"`
	Payload  []byte   `bun:"payload,notnull"`
	Tags     []string `bun:"tags,array,notnull"`
	Optional []byte   `bun:"optional"`
}

func TestRepoCreate_NormalizesNilSliceColumns(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	if _, err := db.Bun().ExecContext(ctx, `CREATE TABLE blob_rows (
		id TEXT PRIMARY KEY,
		payload BYTEA NOT NULL DEFAULT '\x',
		tags TEXT[] NOT NULL DEFAULT '{}',
		optional BYTEA
	)`); err != nil {
		t.Fatalf("create blob_rows: %v", err)
	}

	repo := NewRepo[blobRow]()

	// Payload and Tags left nil on purpose: the NOT NULL columns must
	// receive their empty value, not NULL.
	row := &blobRow{ID: "b1"}
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("Create with a nil NOT NULL bytea column: %v", err)
	}

	got, err := repo.Get(ctx, db, "b1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Payload == nil {
		t.Error("a nil NOT NULL bytea column was not normalized to an empty slice")
	}
	if len(got.Payload) != 0 {
		t.Errorf("payload = %#v, want empty", got.Payload)
	}
	if got.Tags == nil {
		t.Error("a nil NOT NULL array column was not normalized to an empty slice")
	}
	// The NULLABLE bytea keeps its nil: "unset" stays distinguishable from
	// "empty", which is the whole reason the column was declared nullable.
	if got.Optional != nil {
		t.Errorf("nullable bytea = %#v, want nil — normalizing it would erase the absent/empty distinction", got.Optional)
	}

	// A real value round-trips byte for byte, including a leading NUL.
	payload := []byte{0x00, 0xff, 0x7f, 0x01}
	if err := repo.Create(ctx, db, &blobRow{ID: "b2", Payload: payload}); err != nil {
		t.Fatalf("Create with a payload: %v", err)
	}
	back, err := repo.Get(ctx, db, "b2")
	if err != nil {
		t.Fatalf("Get b2: %v", err)
	}
	if !bytes.Equal(back.Payload, payload) {
		t.Errorf("payload = %#v, want %#v", back.Payload, payload)
	}
}

// TestRepoUpdateMasked_NormalizesOnlyMaskedSliceColumns pins that the
// masked write keeps the same selector: a masked column is normalized, an
// unmasked one is not touched at all.
func TestRepoUpdateMasked_NormalizesOnlyMaskedSliceColumns(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	if _, err := db.Bun().ExecContext(ctx, `CREATE TABLE blob_rows (
		id TEXT PRIMARY KEY,
		payload BYTEA NOT NULL DEFAULT '\x',
		tags TEXT[] NOT NULL DEFAULT '{}',
		optional BYTEA
	)`); err != nil {
		t.Fatalf("create blob_rows: %v", err)
	}
	repo := NewRepo[blobRow]()

	seed := &blobRow{ID: "m1", Payload: []byte{0x01}, Tags: []string{"a"}}
	if err := repo.Create(ctx, db, seed); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Clear payload via the mask with a nil field: it must land as the
	// empty value, not violate NOT NULL.
	if err := repo.UpdateMasked(ctx, db, &blobRow{ID: "m1"}, []string{"payload"}); err != nil {
		t.Fatalf("UpdateMasked payload: %v", err)
	}
	got, err := repo.Get(ctx, db, "m1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Payload) != 0 {
		t.Errorf("masked payload = %#v, want empty", got.Payload)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "a" {
		t.Errorf("tags = %v, want the unmasked column untouched", got.Tags)
	}
}

var _ = orm.ErrNoRows
