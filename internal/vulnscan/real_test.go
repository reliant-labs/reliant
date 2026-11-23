package vulnscan

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// realGrypeEnv opts in to TestRealGrype: it downloads the pinned grype
// (~30 MB) and, into $FORGE_VULN_DB_DIR (default the shared cache), the
// vulnerability database (~200 MB, about two minutes to import) when no
// current one is there.
const realGrypeEnv = "FORGE_VULN_REAL_GRYPE"

// TestRealGrype runs the real pinned grype against the real database: the
// flags, the environment, `db status` and the JSON this package parses are
// those of the binary forge ships, not a fake's. Opt-in, because its first
// run downloads ~230 MB.
func TestRealGrype(t *testing.T) {
	testkit.Slow(t, "installs the pinned grype and the ~200 MB vulnerability database, then scans")
	if os.Getenv(realGrypeEnv) != "1" {
		t.Skipf("set %s=1 to run grype for real (downloads ~230 MB on first use)", realGrypeEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	db, err := Open(func(ctx context.Context) (string, string, error) { return toolfetch.Ensure(ctx, toolfetch.Grype) })
	if err != nil {
		t.Fatal(err)
	}
	db.Log = testWriter{t}
	st, err := db.Ensure(ctx, EnsureOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	// The wall clock on purpose: the real database's age is measured
	// against now, and Ensure has just made it current.
	if !st.Present || st.Schema == "" || st.Stale(time.Now()) {
		t.Fatalf("status after Ensure = %+v", st)
	}
	sbom, err := os.ReadFile("testdata/ws-8.20.0.cdx.json")
	if err != nil {
		t.Fatal(err)
	}
	scan, err := (&Scanner{DB: db}).ScanSBOM(ctx, sbom)
	if err != nil {
		t.Fatal(err)
	}
	if !scan.DBBuiltAt.Equal(st.BuiltAt) || scan.ScannerVersion != toolfetch.Grype.Version {
		t.Errorf("scan record = db %s version %s, want db %s version %s", scan.DBBuiltAt, scan.ScannerVersion, st.BuiltAt, toolfetch.Grype.Version)
	}
	found := false
	for _, f := range scan.Findings {
		if f.Names("GHSA-96hv-2xvq-fx4p") && f.Package.Name == "ws" && f.Fixable() && f.Confidence == ConfidenceHigh {
			found = true
		}
	}
	if !found {
		t.Fatalf("ws 8.20.0: GHSA-96hv-2xvq-fx4p not found in %+v", scan.Findings)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
