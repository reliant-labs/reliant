package dbtrace

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/querycapture"
)

// With FORGE_QUERY_CAPTURE_DIR set, NewPgxTracer also records the shape of
// each statement and each batched statement, for `forge db analyze`; unset,
// it records nothing.
func TestPgxTracerCapturesQueryShapes(t *testing.T) {
	t.Setenv(querycapture.EnvDir, "")
	if NewPgxTracer().capture != nil {
		t.Fatal("unset: want no capture")
	}

	dir := t.TempDir()
	t.Setenv(querycapture.EnvDir, dir)
	pt := NewPgxTracer()
	// Close the capture file before t.TempDir's cleanup removes the
	// directory: Windows refuses to delete a file that is still open.
	// (Cleanups run last-registered first.)
	t.Cleanup(func() { _ = pt.capture.Close() })
	ctx := context.Background()
	pt.TraceQueryEnd(pt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT id FROM chats WHERE id IN ($1, $2)"}), nil, pgx.TraceQueryEndData{})
	pt.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "UPDATE chats SET title = $1 WHERE id = $2"})
	pt.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "BEGIN"})

	c, err := querycapture.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range c.Shapes {
		got = append(got, s.SQL)
		if len(s.Callers) != 1 || len(s.Callers[0].Tests) != 1 || s.Callers[0].Tests[0] != "TestPgxTracerCapturesQueryShapes" {
			t.Errorf("%s: callers %+v, want this test", s.SQL, s.Callers)
		}
	}
	want := map[string]bool{"SELECT id FROM chats WHERE id IN ($1)": true, "UPDATE chats SET title = $1 WHERE id = $2": true}
	if len(got) != len(want) || !want[got[0]] || !want[got[1]] {
		t.Fatalf("captured %q, want the SELECT and the UPDATE (BEGIN is not a query shape)", got)
	}
}
