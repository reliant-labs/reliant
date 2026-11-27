package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const cpEndpoint = "https://cp.example"

func TestClaimablePublished(t *testing.T) {
	tests := []struct {
		name string
		c    Claimable
		want bool
	}{
		{"nothing published", Claimable{ClaimURL: "https://claim"}, false},
		{"a failed attempt alone is not a publish", Claimable{LastAttemptAt: time.Now(), LastAttemptError: "boom"}, false},
		{"a deploy from this machine published", Claimable{LastPublishedAt: time.Now()}, true},
		{"a live address was observed", Claimable{URL: "https://site.example"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.Published(); got != tt.want {
				t.Errorf("Published() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEndpointsAreSortedAndNormalizedKeys(t *testing.T) {
	f := &File{}
	if got := f.Endpoints(); len(got) != 0 {
		t.Fatalf("Endpoints() of an empty file = %v", got)
	}
	for _, ep := range []string{"https://zeta.example", "https://alpha.example:8443", "HTTPS://Mid.example:443/"} {
		if err := f.Put(ep, "human", Credential{Token: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"https://alpha.example:8443", "https://mid.example", "https://zeta.example"}
	if got := f.Endpoints(); !reflect.DeepEqual(got, want) {
		t.Errorf("Endpoints() = %v, want %v", got, want)
	}
}

func TestFileAccessorsRejectBadEndpoints(t *testing.T) {
	f := &File{}
	bad := "ftp://cp.example"
	if _, err := f.Get(bad, "human"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get with a bad endpoint: err = %v, want a validation error", err)
	}
	if err := f.Put(bad, "human", Credential{Token: "t"}); err == nil {
		t.Error("Put accepted a non-http(s) endpoint")
	}
	if _, err := f.Delete(bad, "human"); err == nil {
		t.Error("Delete accepted a non-http(s) endpoint")
	}
}

func TestPutRefusesBlankClientAndInitializesNilMaps(t *testing.T) {
	f := &File{}
	if err := f.Put(cpEndpoint, "  ", Credential{Token: "t"}); err == nil || !strings.Contains(err.Error(), "client id") {
		t.Errorf("blank client: err = %v, want a client-id error", err)
	}
	if err := f.Put(cpEndpoint, "human", Credential{Token: "t"}); err != nil {
		t.Fatalf("Put into a zero File: %v", err)
	}
	got, err := f.Get(cpEndpoint, "human")
	if err != nil || got.Token != "t" {
		t.Errorf("Get = %+v, %v", got, err)
	}
}

func TestGetTreatsABlankStoredTokenAsAbsent(t *testing.T) {
	f := &File{Credentials: map[string]map[string]Credential{cpEndpoint: {"human": {Token: "   "}}}}
	if _, err := f.Get(cpEndpoint, "human"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")

	if _, err := Lookup(path, cpEndpoint, "human"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup in a missing file: err = %v, want ErrNotFound", err)
	}
	if err := Store(path, cpEndpoint, "human", Credential{Token: "tok", AccountEmail: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(path, cpEndpoint+"/", "human")
	if err != nil || got.Token != "tok" || got.AccountEmail != "a@example.com" {
		t.Errorf("Lookup = %+v, %v", got, err)
	}
	if _, err := Lookup(path, cpEndpoint, "agent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup of an unstored client: err = %v, want ErrNotFound", err)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(path, cpEndpoint, "human"); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("Lookup of a corrupt file: err = %v, want a parse error", err)
	}
}

func TestLoadReportsAnUnreadablePath(t *testing.T) {
	// A directory where the file should be: ReadFile fails with something
	// other than not-exist, which must not read as "logged out".
	dir := t.TempDir()
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "read") {
		t.Errorf("Load(dir) err = %v, want a read error", err)
	}
}

func TestSaveInitializesNilCredentialsAndStampsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	if err := Save(path, &File{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != formatVersion || f.Credentials == nil || len(f.Credentials) != 0 {
		t.Errorf("loaded %+v, want an empty store at version %d", f, formatVersion)
	}
}

func TestSaveFailures(t *testing.T) {
	t.Run("parent is a regular file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err := Save(filepath.Join(blocker, "credentials.json"), &File{})
		if err == nil || !strings.Contains(err.Error(), "create") {
			t.Errorf("err = %v, want a create-directory error", err)
		}
	})

	t.Run("destination is a directory", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "credentials.json")
		if err := os.Mkdir(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		err := Save(dest, &File{})
		if err == nil || !strings.Contains(err.Error(), "replace") {
			t.Errorf("err = %v, want a replace error", err)
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".credentials-") {
				t.Errorf("failed Save leaked temp file %s", e.Name())
			}
		}
	})
}

func TestUpdate(t *testing.T) {
	t.Run("applies the mutation and persists it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		err := Update(path, func(f *File) error { return f.Put(cpEndpoint, "human", Credential{Token: "tok"}) })
		if err != nil {
			t.Fatal(err)
		}
		if got, err := Lookup(path, cpEndpoint, "human"); err != nil || got.Token != "tok" {
			t.Errorf("Lookup = %+v, %v", got, err)
		}
	})

	t.Run("a mutation that changes nothing does not write the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := Update(path, func(*File) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat err = %v; a no-op Update must not create the file", err)
		}
	})

	t.Run("a failing mutation returns its error and leaves the file alone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := Store(path, cpEndpoint, "human", Credential{Token: "keep"}); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("boom")
		err := Update(path, func(f *File) error {
			_ = f.Put(cpEndpoint, "human", Credential{Token: "clobbered"})
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if got, _ := Lookup(path, cpEndpoint, "human"); got.Token != "keep" {
			t.Errorf("token = %q; a failed mutation must not be saved", got.Token)
		}
	})

	t.Run("a corrupt file stops the transaction before the callback", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
			t.Fatal(err)
		}
		called := false
		err := Update(path, func(*File) error { called = true; return nil })
		if err == nil || called {
			t.Errorf("err = %v called = %v, want a parse error and no callback", err, called)
		}
	})

	t.Run("an unusable directory fails to lock", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		called := false
		err := Update(filepath.Join(blocker, "credentials.json"), func(*File) error { called = true; return nil })
		if err == nil || called {
			t.Errorf("err = %v called = %v, want a lock error and no callback", err, called)
		}
	})
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := Store(path, cpEndpoint, "human", Credential{Token: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := Store(path, cpEndpoint, "agent", Credential{Token: "a"}); err != nil {
		t.Fatal(err)
	}

	if removed, err := Remove(path, cpEndpoint, "ghost"); err != nil || removed {
		t.Errorf("Remove of an unstored client = %v, %v; want false, nil", removed, err)
	}
	if removed, err := Remove(path, cpEndpoint, "human"); err != nil || !removed {
		t.Fatalf("Remove = %v, %v; want true, nil", removed, err)
	}
	if got, err := Lookup(path, cpEndpoint, "agent"); err != nil || got.Token != "a" {
		t.Errorf("the other client was disturbed: %+v, %v", got, err)
	}
	if _, err := Remove(path, "gopher://x", "agent"); err == nil {
		t.Error("Remove accepted a bad endpoint")
	}

	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(path, cpEndpoint, "agent"); err == nil {
		t.Error("Remove on a corrupt file returned no error")
	}
}

func TestRemoveRevokedWhenNothingIsStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	removed, err := RemoveRevoked(path, cpEndpoint, "human", "A")
	if err != nil || removed {
		t.Errorf("RemoveRevoked with no stored credential = %v, %v; want false, nil", removed, err)
	}
	if _, err := RemoveRevoked(path, "gopher://x", "human", "A"); err == nil {
		t.Error("RemoveRevoked accepted a bad endpoint")
	}
}
