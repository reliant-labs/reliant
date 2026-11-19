package devidp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// seenTokens records the bearer token of each request, in order.
type seenTokens struct {
	mu   sync.Mutex
	list []string
}

func (s *seenTokens) add(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, v)
}

func (s *seenTokens) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) == 0 {
		return ""
	}
	return s.list[len(s.list)-1]
}

func tokenServer(t *testing.T) (*httptest.Server, *seenTokens) {
	t.Helper()
	seen := &seenTokens{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.add(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"result":[{"id":"o1","name":"org"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func callOnce(t *testing.T, c *Client) {
	t.Helper()
	if _, err := c.DefaultOrg(context.Background()); err != nil {
		t.Fatalf("call: %v", err)
	}
}

func writeToken(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The file is re-read on every use, so a rotated token is presented on the very
// next call with no restart and no polling delay.
func TestTokenFile_RotationIsPickedUpOnNextUse(t *testing.T) {
	srv, seen := tokenServer(t)
	path := filepath.Join(t.TempDir(), "broker-token")
	writeToken(t, path, "token-v1\n") // a trailing newline is what a Secret volume or an editor leaves

	c := &Client{Base: srv.URL, TokenFile: path}
	callOnce(t, c)
	if got := seen.last(); got != "Bearer token-v1" {
		t.Fatalf("first call presented %q", got)
	}

	writeToken(t, path, "token-v2")
	callOnce(t, c)
	if got := seen.last(); got != "Bearer token-v2" {
		t.Fatalf("after rotation the call presented %q, want the new token", got)
	}
}

// A Kubernetes Secret volume updates by atomically swapping a symlink; so does
// a rename-into-place write.
func TestTokenFile_FollowsAtomicReplace(t *testing.T) {
	srv, seen := tokenServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "broker-token")
	writeToken(t, path, "token-v1")
	c := &Client{Base: srv.URL, TokenFile: path}
	callOnce(t, c)

	tmp := filepath.Join(dir, "broker-token.tmp")
	writeToken(t, tmp, "token-v2")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	callOnce(t, c)
	if got := seen.last(); got != "Bearer token-v2" {
		t.Fatalf("after an atomic replace the call presented %q", got)
	}
}

// The env var form stays supported, alone and as the fallback.
func TestTokenFile_EnvTokenStillWorksAndIsTheFallback(t *testing.T) {
	srv, seen := tokenServer(t)

	callOnce(t, &Client{Base: srv.URL, Token: "env-token"})
	if got := seen.last(); got != "Bearer env-token" {
		t.Fatalf("plain Token: presented %q", got)
	}

	// The file does not exist and has never been read: the env token serves.
	callOnce(t, &Client{Base: srv.URL, Token: "env-token", TokenFile: filepath.Join(t.TempDir(), "absent")})
	if got := seen.last(); got != "Bearer env-token" {
		t.Fatalf("missing file should fall back to Token, presented %q", got)
	}

	// The file wins while it yields a token.
	path := filepath.Join(t.TempDir(), "broker-token")
	writeToken(t, path, "file-token")
	callOnce(t, &Client{Base: srv.URL, Token: "env-token", TokenFile: path})
	if got := seen.last(); got != "Bearer file-token" {
		t.Fatalf("the file should win over Token, presented %q", got)
	}
}

// A file that disappears or is emptied mid-rotation must not take sign-in down:
// the last token read keeps being presented, and recovery is automatic.
func TestTokenFile_UnusableFileKeepsLastToken(t *testing.T) {
	srv, seen := tokenServer(t)
	path := filepath.Join(t.TempDir(), "broker-token")
	writeToken(t, path, "token-v1")
	c := &Client{Base: srv.URL, TokenFile: path}
	callOnce(t, c)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	callOnce(t, c)
	if got := seen.last(); got != "Bearer token-v1" {
		t.Fatalf("a missing file must keep the last token, presented %q", got)
	}

	writeToken(t, path, " \n")
	callOnce(t, c)
	if got := seen.last(); got != "Bearer token-v1" {
		t.Fatalf("an empty file must keep the last token, presented %q", got)
	}

	writeToken(t, path, "token-v2")
	callOnce(t, c)
	if got := seen.last(); got != "Bearer token-v2" {
		t.Fatalf("the file recovered but %q was presented", got)
	}
}

func TestTokenFile_HasBrokerToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker-token")
	if (&Client{TokenFile: path}).HasBrokerToken() {
		t.Fatal("a missing file and no Token must report no token")
	}
	writeToken(t, path, "t")
	if !(&Client{TokenFile: path}).HasBrokerToken() {
		t.Fatal("a file holding a token must report one")
	}
	if !(&Client{Token: "x"}).HasBrokerToken() {
		t.Fatal("Token alone must report one")
	}
}
