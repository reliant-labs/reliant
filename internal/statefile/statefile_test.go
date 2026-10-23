package statefile

import (
	"os"
	"path/filepath"
	"testing"
)

type rec struct {
	Image string `json:"image"`
	Tag   string `json:"tag"`
}

// TestWriteReadRoundTrip locks the on-disk contract the three callers
// depend on: 2-space-indented JSON under .forge/state, created lazily,
// round-tripping back to an equal value.
func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, "build-prod.json")

	if err := Write(path, "test state", rec{Image: "img", Tag: "v1"}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Directory is created lazily under .forge/state.
	if got := filepath.Dir(path); filepath.Base(filepath.Dir(got)) != ".forge" || filepath.Base(got) != "state" {
		t.Fatalf("unexpected dir layout: %q", got)
	}

	// Byte format: indented JSON, exactly what MarshalIndent(_, "", "  ")
	// produced in the legacy impls, so existing files keep loading.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "{\n  \"image\": \"img\",\n  \"tag\": \"v1\"\n}"
	if string(data) != want {
		t.Fatalf("on-disk format drift:\n got: %q\nwant: %q", string(data), want)
	}

	st, err := Read[rec](path, "test state")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st == nil || st.Image != "img" || st.Tag != "v1" {
		t.Fatalf("round-trip mismatch: %+v", st)
	}
}

// TestReadMissingIsNilNil keeps the "no previous record" semantics every
// caller relies on: a missing file is (nil, nil), not an error.
func TestReadMissingIsNilNil(t *testing.T) {
	st, err := Read[rec](Path(t.TempDir(), "absent.json"), "test state")
	if err != nil {
		t.Fatalf("Read missing: unexpected err %v", err)
	}
	if st != nil {
		t.Fatalf("Read missing: want nil, got %+v", st)
	}
}

// TestSafeSegmentStripsSeparators is the path-traversal guard: a segment
// carrying path separators can never escape .forge/state.
func TestSafeSegmentStripsSeparators(t *testing.T) {
	cases := map[string]string{
		"dev":         "dev",
		"my-svc_1":    "my-svc_1",
		"../../etc":   "______etc",
		"a/b":         "a_b",
		"":            "_",
		"a\\b":        "a_b",
		"weird name!": "weird_name_",
	}
	for in, want := range cases {
		if got := SafeSegment(in); got != want {
			t.Errorf("SafeSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
