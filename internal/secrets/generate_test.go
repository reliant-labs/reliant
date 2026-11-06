package secrets

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateSpecEncodings(t *testing.T) {
	h, err := GenerateSpec{Bytes: 16, Encoding: "hex", Prefix: "k_"}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(h, "k_"))
	if err != nil || len(raw) != 16 || !strings.HasPrefix(h, "k_") {
		t.Fatalf("bad hex value %q", h)
	}
	for _, bad := range []GenerateSpec{{Bytes: 0, Encoding: "hex"}, {Bytes: 8, Encoding: "rot13"}} {
		if _, err := bad.Generate(); err == nil {
			t.Fatalf("%+v should be rejected", bad)
		}
	}
}

func TestEnsureGeneratedSkipsKeysPresentInProviderView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.yaml")
	got, err := EnsureGenerated(path, map[string]GenerateSpec{"K": {Bytes: 8, Encoding: "hex"}}, []string{"K"}, map[string]string{"K": "inherited"})
	if err != nil || len(got) != 0 {
		t.Fatalf("generated %v err %v despite an inherited value", got, err)
	}
}
