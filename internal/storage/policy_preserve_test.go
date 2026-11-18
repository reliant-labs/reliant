package storage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSavePreservesRetentionKeysUnknownToThisVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	p := DefaultPolicy()
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["go_cache_min_age"] = json.RawMessage(`"2h"`)
	raw["future_retention"] = json.RawMessage(`{"floor":"24h","keep":9007199254740993}`)
	b, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	// Save even a freshly constructed policy must not erase machine settings
	// understood by other versions. Converge uses this same writer under its lock.
	p.GoCacheGiB = 41
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"go_cache_min_age", "future_retention"} {
		var compact bytes.Buffer
		if err := json.Compact(&compact, got[key]); err != nil {
			t.Errorf("%s lost or invalid: %v", key, err)
			continue
		}
		if compact.String() != string(raw[key]) {
			t.Errorf("%s lost or changed: got %s want %s", key, got[key], raw[key])
		}
	}
	if string(got["go_cache_gib"]) != "41" {
		t.Fatalf("known budget update lost: %s", got["go_cache_gib"])
	}
}

func TestConvergePreservesLegacyDaemonRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	b, err := json.Marshal(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	raw["go_cache_min_age"] = json.RawMessage(`"2h"`)
	b, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Converge(path, Facts{Pins: []string{"sha256:retention-test"}}); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The legacy daemon understands this field; current Forge does not.
	var legacy struct {
		GoCacheMinAge string `json:"go_cache_min_age"`
	}
	if err := json.Unmarshal(b, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.GoCacheMinAge != "2h" {
		t.Fatalf("new registration reset legacy daemon retention to its unsafe default: %q", legacy.GoCacheMinAge)
	}
}

func TestSaveDoesNotResurrectOmittedKnownSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	p := DefaultPolicy()
	p.HostPaths = []string{"/old"}
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	p.HostPaths = nil
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.HostPaths) != 0 {
		t.Fatalf("known setting resurrected: %v", got.HostPaths)
	}
}

func TestSaveRefusesToEraseUnreadablePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	before := []byte(`{"go_cache_min_age":"2h",`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, DefaultPolicy()); err == nil {
		t.Fatal("must not overwrite a policy whose unknown settings cannot be read")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("invalid existing policy was overwritten")
	}
}
