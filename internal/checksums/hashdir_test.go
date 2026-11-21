package checksums

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeT(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readT(t *testing.T, root, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

// TestHashCerts_OneFilePerOutput: each comment-incapable output's
// certificate is its own file, so changing one output's hash rewrites that
// file and no other — the property that keeps parallel branches from
// conflicting in a shared manifest.
func TestHashCerts_OneFilePerOutput(t *testing.T) {
	root := t.TempDir()
	a, b := Hash([]byte("a")), Hash([]byte("b"))
	cs := &FileChecksums{Unstampable: map[string]string{
		"deploy/spec/a_gen.json":   a,
		"deploy/spec/b_gen.ndjson": b,
	}}
	if err := Save(root, cs); err != nil {
		t.Fatal(err)
	}
	if got, _ := readT(t, root, ".forge/hashes/deploy/spec/a_gen.json.sha256"); got != a+"\n" {
		t.Errorf("a's certificate = %q, want %q", got, a+"\n")
	}
	bBefore, _ := os.Stat(filepath.Join(root, ".forge/hashes/deploy/spec/b_gen.ndjson.sha256"))

	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Unstampable) != 2 || loaded.Unstampable["deploy/spec/a_gen.json"] != a {
		t.Fatalf("round trip: %v", loaded.Unstampable)
	}

	// Re-render a only: b's certificate is untouched.
	loaded.Unstampable["deploy/spec/a_gen.json"] = Hash([]byte("a2"))
	if err := Save(root, loaded); err != nil {
		t.Fatal(err)
	}
	bAfter, _ := os.Stat(filepath.Join(root, ".forge/hashes/deploy/spec/b_gen.ndjson.sha256"))
	if !bAfter.ModTime().Equal(bBefore.ModTime()) {
		t.Error("saving a's new hash rewrote b's certificate")
	}

	// Retire both: their certificates and the emptied directories go.
	loaded.Unstampable = map[string]string{}
	if err := Save(root, loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, HashesDir)); !os.IsNotExist(err) {
		t.Errorf("an empty certificate tree must be removed (stat err=%v)", err)
	}
}

// TestHashCerts_CorruptCertificateFailsLoad: a certificate that is not a
// hash is named, not silently ignored (an ignored one would read as an
// uncertified output and be overwritten).
func TestHashCerts_CorruptCertificateFailsLoad(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, HashCertPath("a_gen.json"), "<<<<<<< ours\n")
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "a_gen.json.sha256") {
		t.Errorf("Load err = %v, want one naming the certificate", err)
	}
}

func TestHashCertPathRoundTrip(t *testing.T) {
	for _, rel := range []string{"a_gen.json", "deploy/observability/spec/x_gen.json"} {
		got, ok := HashCertTarget(HashCertPath(rel))
		if !ok || got != rel {
			t.Errorf("HashCertTarget(HashCertPath(%q)) = %q, %v", rel, got, ok)
		}
	}
	for _, notCert := range []string{".forge/hashes.json", ".forge/hashes/a.json", "x/.forge/hashes/a.sha256", ".forge/hashes/../a.sha256"} {
		if IsHashCert(notCert) {
			t.Errorf("IsHashCert(%q) = true", notCert)
		}
	}
	if !IsCommittedState(HashCertPath("a_gen.json")) || !IsCommittedState(DisownedFile) || IsCommittedState(".forge/forge.lock") {
		t.Error("IsCommittedState misclassifies")
	}
}

// TestMergeStateFile_Certificate: the driver merges a certificate as one
// value: a side that changed it wins over one that did not, and when both
// changed it this side's stands (the driver keeps this side's render).
func TestMergeStateFile_Certificate(t *testing.T) {
	base, o, th := Hash([]byte("base"))+"\n", Hash([]byte("ours"))+"\n", Hash([]byte("theirs"))+"\n"
	for _, tc := range []struct {
		name, base, ours, theirs, want string
	}{
		{"theirs changed", base, base, th, th},
		{"ours changed", base, o, base, o},
		{"both changed", base, o, th, o},
		{"added on both", "", o, th, o},
	} {
		for _, rel := range []string{HashCertPath("a_gen.json"), "apps/web/" + HashCertPath("a_gen.json")} {
			got, ok := MergeStateFile(rel, []byte(tc.base), []byte(tc.ours), []byte(tc.theirs))
			if !ok || string(got) != tc.want {
				t.Errorf("%s (%s): got %q, %v; want %q", tc.name, rel, got, ok, tc.want)
			}
		}
	}
	if _, ok := MergeStateFile(HashCertPath("a_gen.json"), []byte(base), []byte("hand edit\n"), []byte(th)); ok {
		t.Error("a side that is not a hash must fall back to the text merge")
	}
}
