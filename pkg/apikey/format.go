package apikey

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxPrefixLen bounds a [Format]'s prefix, underscores included. A prefix is
// a label for people and secret scanners, not entropy.
const MaxPrefixLen = 16

// sessionIDPrefix is forge/pkg/session's IDPrefix. A server routes a
// credential by its shape, so a key prefix equal to it would send session
// ids to the key validator. It is spelled here rather than
// imported so this package stays free of the session package's dependencies;
// a test holds the two equal.
const sessionIDPrefix = "fs_"

// The final underscore separates the prefix from the base62 random part.
// No two permitted prefixes overlap.
var prefixPattern = regexp.MustCompile(`^[a-z][a-z0-9]*_$`)

// Format is the shape of the API keys an application issues and accepts: a
// prefix, then 32 random base62 characters. The prefix makes a key
// recognisable in logs and to secret scanners, and lets a server route a
// credential to its key validator by shape.
//
// The zero value is forge's default, [KeyPrefix] ("fk_"), so the package
// functions [Generate] and [LookupPrefix] are the zero Format's methods. A
// product that hands keys to its own users names them with [NewFormat]:
//
//	var keys = apikey.MustFormat("acme_")
//	key, err := keys.Generate()            // "acme_…"
//	prefix, err := keys.LookupPrefix(key)  // indexed lookup handle
//
// Every Format also ACCEPTS keys with [KeyPrefix], so keys issued before a
// project first chose a custom prefix keep authenticating. It does not
// accept arbitrary prior custom prefixes. A Format's lookup handle is the
// same LookupPrefixLen characters after the prefix whichever
// prefix a key carries, so a table of keys needs no migration.
//
// Install the same Format on the server with
// [github.com/reliant-labs/forge/pkg/auth.Authenticator.UseKeyFormat], so a
// key the application issues is routed to its key validator.
type Format struct {
	prefix string
}

// NewFormat returns the Format whose keys start with prefix. A prefix is a
// lowercase letter followed by zero or more lowercase letters or digits and
// a final "_" (such as "acme_"), at most [MaxPrefixLen] bytes in total. The
// forge session ID prefix ("fs_") is reserved.
func NewFormat(prefix string) (Format, error) {
	switch {
	case len(prefix) > MaxPrefixLen:
		return Format{}, fmt.Errorf("apikey: key prefix %q is longer than %d bytes", prefix, MaxPrefixLen)
	case !prefixPattern.MatchString(prefix):
		return Format{}, fmt.Errorf("apikey: key prefix %q must be lowercase letters and digits ending in \"_\", starting with a letter (e.g. \"acme_\")", prefix)
	case prefix == sessionIDPrefix:
		return Format{}, fmt.Errorf("apikey: key prefix %q is taken by session ids (%q)", prefix, sessionIDPrefix)
	}
	return Format{prefix: prefix}, nil
}

// MustFormat is [NewFormat] for a prefix fixed in code; it panics on an
// invalid one, as regexp.MustCompile does.
func MustFormat(prefix string) Format {
	f, err := NewFormat(prefix)
	if err != nil {
		panic(err)
	}
	return f
}

// Prefix returns the prefix f issues keys with.
func (f Format) Prefix() string {
	if f.prefix == "" {
		return KeyPrefix
	}
	return f.prefix
}

// Generate returns a new random key: f's prefix and 32 base62 characters
// (~190 bits). The plaintext is returned once; store only its [Hash] and
// lookup prefix.
func (f Format) Generate() (string, error) {
	return generate(f.Prefix())
}

// LookupPrefix returns the indexed lookup handle for key: the LookupPrefixLen
// characters after its prefix, which is f's or [KeyPrefix]. It doubles as a
// cheap format check: anything else is rejected before any DB work.
func (f Format) LookupPrefix(key string) (string, error) {
	return lookupPrefix(key, f.match(key))
}

// IsKey reports whether credential is shaped like a key f accepts — it
// starts with f's prefix or [KeyPrefix] — and so belongs to the key
// validator rather than a token validator. It checks only the prefix, not
// the length: a credential too short to be a key is still a key's to
// reject, never a token's to try.
func (f Format) IsKey(credential string) bool {
	return f.match(credential) != ""
}

// match returns the accepted prefix credential starts with, or "".
func (f Format) match(credential string) string {
	if p := f.Prefix(); strings.HasPrefix(credential, p) {
		return p
	}
	if strings.HasPrefix(credential, KeyPrefix) {
		return KeyPrefix
	}
	return ""
}
