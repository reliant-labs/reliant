package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDirs_PathPrecedence(t *testing.T) {
	for _, tc := range []struct {
		d    Dirs
		want string
	}{
		// FromSlash: Path joins with the host separator.
		{Dirs{ForgeHome: "/fh", XDGConfigHome: "/xdg", Home: "/h"}, filepath.FromSlash("/fh/credentials.json")},
		{Dirs{XDGConfigHome: "/xdg", Home: "/h"}, filepath.FromSlash("/xdg/forge/credentials.json")},
		{Dirs{Home: "/h"}, filepath.FromSlash("/h/.config/forge/credentials.json")},
	} {
		if got, err := tc.d.Path(); err != nil || got != tc.want {
			t.Errorf("%+v: got %q %v, want %q", tc.d, got, err, tc.want)
		}
	}
	if _, err := (Dirs{}).Path(); err == nil {
		t.Error("no home at all must be an error, not a relative path")
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"https://Admin.Example.com/":       "https://admin.example.com",
		"https://admin.example.com:443/x":  "https://admin.example.com",
		"http://127.0.0.1:8090":            "http://127.0.0.1:8090",
		"http://localhost:80/":             "http://localhost",
		"  https://a.example.com?q=1#frag": "https://a.example.com",
		"http://[::1]:9000/":               "http://[::1]:9000",
	} {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "admin.example.com", "ftp://x", "https://"} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) must fail", bad)
		}
	}
}

// TestStore_TwoEndpointsCoexist_RemoveTouchesOne is the file's core promise:
// one file, many endpoints, and every write preserves the others.
func TestStore_TwoEndpointsCoexist_RemoveTouchesOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Store(path, "https://a.example.com", "forge-cli", Credential{Token: "ta"}); err != nil {
		t.Fatal(err)
	}
	if err := Store(path, "http://127.0.0.1:8090", "forge-cli", Credential{Token: "tb"}); err != nil {
		t.Fatal(err)
	}
	a, err := Lookup(path, "https://A.example.com/", "forge-cli")
	if err != nil || a.Token != "ta" {
		t.Fatalf("a: %+v %v", a, err)
	}
	b, err := Lookup(path, "http://127.0.0.1:8090", "forge-cli")
	if err != nil || b.Token != "tb" {
		t.Fatalf("b: %+v %v", b, err)
	}

	existed, err := Remove(path, "https://a.example.com", "forge-cli")
	if err != nil || !existed {
		t.Fatalf("remove a: %v %v", existed, err)
	}
	if _, err := Lookup(path, "https://a.example.com", "forge-cli"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a must be gone: %v", err)
	}
	if b, _ := Lookup(path, "http://127.0.0.1:8090", "forge-cli"); b.Token != "tb" {
		t.Fatal("removing a must leave b")
	}
	if existed, _ := Remove(path, "https://a.example.com", "forge-cli"); existed {
		t.Fatal("a second remove reports nothing removed")
	}
}

func TestSave_ModesAndAtomicity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, FileName)
	f := &File{}
	if err := f.Put("https://a.example.com", "forge-cli", Credential{Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	// POSIX mode bits only: Windows reports 0666/0777 regardless of the mode
	// asked for, and protects the user's config directory with its ACL.
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode %o; a bearer credential must be 0600", info.Mode().Perm())
		}
		dinfo, _ := os.Stat(dir)
		if dinfo.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %o; want 0700", dinfo.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("no temp file may be left behind; got %d entries", len(entries))
	}
}

func TestLoad_RefusesNewerFormatAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	newer := filepath.Join(dir, "n.json")
	_ = os.WriteFile(newer, []byte(`{"version": 99, "credentials": {}}`), 0o600)
	if _, err := Load(newer); err == nil {
		t.Error("a newer format must be refused, not partially read")
	}
	corrupt := filepath.Join(dir, "c.json")
	_ = os.WriteFile(corrupt, []byte(`{`), 0o600)
	if _, err := Load(corrupt); err == nil {
		t.Error("a corrupt file must be an error, not an empty store")
	}
	if f, err := Load(filepath.Join(dir, "missing.json")); err != nil || len(f.Credentials) != 0 {
		t.Errorf("a missing file is an empty store: %v", err)
	}
}

func TestPut_RefusesEmptyToken(t *testing.T) {
	if err := (&File{}).Put("https://a.example.com", "forge-cli", Credential{Token: " "}); err == nil {
		t.Fatal("an empty token must not be stored")
	}
}

func TestExpired(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	if (Credential{}).Expired(now) || (Credential{ExpiresAt: &future}).Expired(now) || !(Credential{ExpiresAt: &past}).Expired(now) {
		t.Fatal("Expired is wrong")
	}
}

// TestTwoClientsOneEndpoint: forge and reliant both logged in to one origin
// (a dev admin-server serves both APIs) keep separate tokens, and one CLI's
// logout leaves the other's.
func TestTwoClientsOneEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	const ep = "http://127.0.0.1:8090"
	if err := Store(path, ep, "forge-cli", Credential{Token: "deploy-tok"}); err != nil {
		t.Fatal(err)
	}
	if err := Store(path, ep, "reliant-cli", Credential{Token: "api-tok"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := Lookup(path, ep, "forge-cli"); c.Token != "deploy-tok" {
		t.Fatalf("forge's token was clobbered: %q", c.Token)
	}
	if _, err := Remove(path, ep, "reliant-cli"); err != nil {
		t.Fatal(err)
	}
	if c, _ := Lookup(path, ep, "forge-cli"); c.Token != "deploy-tok" {
		t.Fatal("reliant logout removed forge's token")
	}
	if _, err := Lookup(path, ep, "reliant-cli"); !errors.Is(err, ErrNotFound) {
		t.Fatal("reliant's token must be gone")
	}
}
