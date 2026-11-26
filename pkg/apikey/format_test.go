package apikey

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/session"
)

// The key prefix a Format refuses is the session package's: if the two
// drifted, a project could pick a prefix that sends session ids to its key
// validator.
func TestSessionIDPrefixMatchesSessionPackage(t *testing.T) {
	if sessionIDPrefix != session.IDPrefix {
		t.Fatalf("sessionIDPrefix = %q, session.IDPrefix = %q", sessionIDPrefix, session.IDPrefix)
	}
}

func TestFormat_ZeroValueIsForgeDefault(t *testing.T) {
	var f Format
	if got := f.Prefix(); got != KeyPrefix {
		t.Fatalf("zero Format prefix = %q, want %q", got, KeyPrefix)
	}
	k, err := f.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(k, KeyPrefix) || len(k) != len(KeyPrefix)+randomLen {
		t.Fatalf("zero Format key %q: want %q and %d random chars", k, KeyPrefix, randomLen)
	}
	want, err := LookupPrefix(k)
	if err != nil {
		t.Fatalf("LookupPrefix: %v", err)
	}
	if got, err := f.LookupPrefix(k); err != nil || got != want {
		t.Fatalf("zero Format LookupPrefix = %q, %v; want %q", got, err, want)
	}
}

func TestFormat_GeneratesWithItsPrefix(t *testing.T) {
	for _, prefix := range []string{"acme_", "x_"} {
		f := MustFormat(prefix)
		if got := f.Prefix(); got != prefix {
			t.Errorf("Prefix() = %q, want %q", got, prefix)
		}
		k, err := f.Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !strings.HasPrefix(k, prefix) || len(k) != len(prefix)+randomLen {
			t.Fatalf("key %q: want %q and %d random chars", k, prefix, randomLen)
		}
		p, err := f.LookupPrefix(k)
		if err != nil {
			t.Fatalf("LookupPrefix(%q): %v", k, err)
		}
		if p != k[len(prefix):len(prefix)+LookupPrefixLen] {
			t.Errorf("LookupPrefix(%q) = %q, want the %d chars after %q", k, p, LookupPrefixLen, prefix)
		}
		if !f.IsKey(k) {
			t.Errorf("IsKey(%q) = false for its own key", k)
		}
	}
}

// The custom format accepts forge's "fk_" beside its own prefix, with
// the same lookup handle the old key was stored under.
func TestFormat_AcceptsKeysIssuedBeforeARename(t *testing.T) {
	old, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	stored, err := LookupPrefix(old)
	if err != nil {
		t.Fatalf("LookupPrefix: %v", err)
	}
	f := MustFormat("acme_")
	if !f.IsKey(old) {
		t.Fatalf("IsKey(%q) = false: a key issued before the rename no longer routes as a key", old)
	}
	if got, err := f.LookupPrefix(old); err != nil || got != stored {
		t.Fatalf("LookupPrefix(%q) = %q, %v; want the handle it was stored under, %q", old, got, err, stored)
	}
}

func TestFormat_RejectsOtherCredentials(t *testing.T) {
	f := MustFormat("acme_")
	for _, bad := range []string{
		"",
		"acme_",                // prefix only
		"acme_short",           // under LookupPrefixLen after the prefix
		"fk_short",             // the default's prefix, too short
		"other_ABCDEFGH000000", // a prefix this Format does not accept
		"ACME_ABCDEFGH000000",  // prefixes are case-sensitive
		"eyJhbGciOiJSUzI1NiJ9.e30.sig",
		"fs_" + strings.Repeat("A", 43),
	} {
		if _, err := f.LookupPrefix(bad); err == nil {
			t.Errorf("LookupPrefix(%q) expected error", bad)
		}
	}
	for _, notKey := range []string{"", "eyJhbGciOiJSUzI1NiJ9.e30.sig", "fs_" + strings.Repeat("A", 43), "other_x", "acm_x"} {
		if f.IsKey(notKey) {
			t.Errorf("IsKey(%q) = true", notKey)
		}
	}
	// Too short to be a key, but shaped like one: a key validator's to
	// refuse, never a token validator's to try.
	for _, short := range []string{"acme_", "acme_x", "fk_x"} {
		if !f.IsKey(short) {
			t.Errorf("IsKey(%q) = false", short)
		}
	}
}

func TestNewFormat_Validation(t *testing.T) {
	for _, ok := range []string{"fk_", "acme_", "a_", "a1b2_", "abcdefghijklmno_"} {
		if _, err := NewFormat(ok); err != nil {
			t.Errorf("NewFormat(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"acme",              // no trailing underscore
		"_",                 // no segment
		"_acme_",            // leading underscore
		"acme_live_",        // embedded underscore
		"Acme_",             // uppercase
		"1acme_",            // starts with a digit
		"ac-me_",            // punctuation
		"abcdefghijklmnop_", // 17 bytes
		"fs_",               // session ids
		"fs_live_",          // extends the session prefix
		"eyJ_",              // uppercase, and a JWT's head
		"acme_\n",           // trailing newline
	} {
		if _, err := NewFormat(bad); err == nil {
			t.Errorf("NewFormat(%q) expected error", bad)
		}
	}
}

func TestMustFormat_PanicsOnInvalidPrefix(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustFormat(\"fs_\") did not panic")
		}
	}()
	MustFormat("fs_")
}
