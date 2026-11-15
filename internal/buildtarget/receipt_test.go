package buildtarget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func receiptFixture(t *testing.T) release.BuildReceipt {
	t.Helper()
	d := "sha256:" + strings.Repeat("a", 64)
	in := release.BuildInputs{Recipe: "reliant", Sources: []release.Source{{Repo: "example/reliant", Commit: strings.Repeat("b", 40)}}, ContextDigest: d, RecipeDigest: d, ConfigDigest: d, Tools: map[string]string{"go": d}, Platforms: []string{"linux/amd64"}, Outputs: []string{"reliant"}, Cacheable: false}
	key, err := release.BuildInputDigest(in)
	if err != nil {
		t.Fatal(err)
	}
	return release.BuildReceipt{Inputs: in, InputDigest: key, Outputs: map[string]string{"reliant": d}}
}

func TestReceiptRoundTripAndNoReplace(t *testing.T) {
	r := receiptFixture(t)
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := WriteReceipt(path, r); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReceipt(path, r.InputDigest)
	if err != nil {
		t.Fatal(err)
	}
	if got.Inputs.Sources[0].Commit != r.Inputs.Sources[0].Commit {
		t.Fatal("lost actual source")
	}
	if err := WriteReceipt(path, r); err == nil {
		t.Fatal("overwrote invocation receipt")
	}
	if _, err := ReadReceipt(path, "sha256:"+strings.Repeat("c", 64)); err == nil {
		t.Fatal("accepted changed inputs")
	}
}

func TestReceiptRefusesIncompleteAndConflictingOutputs(t *testing.T) {
	for _, name := range []string{"missing", "extra", "mutable", "tampered", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			r := receiptFixture(t)
			switch name {
			case "missing":
				delete(r.Outputs, "reliant")
			case "extra":
				r.Outputs["api"] = r.Outputs["reliant"]
			case "mutable":
				r.Outputs["reliant"] = "latest"
			case "tampered":
				r.Inputs.Recipe = "other"
			case "duplicate":
				r.Inputs.Outputs = []string{"reliant", "reliant"}
				r.InputDigest, _ = release.BuildInputDigest(r.Inputs)
			}
			if err := ValidateReceipt(r, ""); err == nil {
				t.Fatal("accepted invalid receipt")
			}
		})
	}
}

func TestReceiptStrictBoundedRead(t *testing.T) {
	r := receiptFixture(t)
	data, _ := json.Marshal(r)
	for _, data := range [][]byte{append(data, []byte(" {}")...), []byte(`{"unknown":true}`), []byte(strings.Repeat(" ", maxReceiptBytes+1))} {
		path := filepath.Join(t.TempDir(), "receipt.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadReceipt(path, ""); err == nil {
			t.Fatal("accepted malformed receipt")
		}
	}
}
