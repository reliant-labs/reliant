//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EOrderByWithoutDescending is the acceptance gate for the
// review-found codegen bug: a hand-authored List request that declares
// `string order_by` but NO companion `bool descending` field must still
// scaffold a Tier-1 list handler whose generated app BUILDS. The old
// ops template unconditionally emitted
//
//	OrderBy: func(req *pb.ListWidgetsRequest) (string, bool) {
//	    return req.OrderBy, req.Descending
//	}
//
// which references a field the proto type doesn't have, so `go build`
// of the generated app failed. The fix defaults order to ascending
// (returns a constant false) when the request carries no descending
// field, while still wiring order_by itself.
//
// Runs the REAL pipeline (project new + scaffold + generate) and gates
// on `go build ./...` + `go vet ./...` of the generated app — the
// RED→GREEN this test locks in.
func TestE2EOrderByWithoutDescending(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "widgetsapp", "--mod", "example.com/widgetsapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "widgetsapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author a marked entity and OUR ListWidgetsRequest. The request
	// carries order_by but deliberately OMITS the `bool descending`
	// companion the scaffolder would otherwise emit — quintet completion
	// keeps the message the user already declared, so the order_by-only
	// shape survives into the generated list handler. Standard AIP-158
	// pagination fields keep it a Tier-1 list (not a custom-read stub).
	protoPath := filepath.Join(projectDir, "proto", "services", "widgets", "v1", "widgets.proto")
	proto := readFileE2E(t, protoPath)
	proto += `
// forge:entity
message Widget {
  string id = 1;
  string name = 2;
}

message ListWidgetsRequest {
  int32 page_size = 1;
  string page_token = 2;
  string order_by = 3;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author widgets proto: %v", err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── ops: order_by wired, but NO dereference of the missing field ──
	ops := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "widgets", "handlers_crud_ops_gen.go"))
	if strings.Contains(ops, "req.Descending") {
		t.Errorf("order_by WITHOUT a descending field must NOT reference req.Descending; ops:\n%s", ops)
	}
	if !strings.Contains(ops, "return req.OrderBy, false") {
		t.Errorf("expected the ascending-default OrderBy closure (return req.OrderBy, false); ops:\n%s", ops)
	}
	if !strings.Contains(ops, "HasOrderBy:    true") {
		t.Errorf("order_by is still wired, so HasOrderBy must be true; ops:\n%s", ops)
	}

	// ── the generated app must BUILD and vet clean (the whole point) ──
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
}
