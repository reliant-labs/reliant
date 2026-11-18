package devidp

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// A bearer token that can change while the process runs.
//
// IDP_BROKER_TOKEN is read once, at boot, so a control plane that rotates the
// broker's credential would have to restart every consumer. IDP_BROKER_TOKEN_FILE
// (Client.TokenFile) names a file holding the token instead, and every call
// reads it: the file is a few hundred bytes and a login is not a hot path, so
// there is nothing to cache and no change detector to get wrong. A Kubernetes
// Secret volume updates by swapping a symlink, which an open-read-close per use
// always sees; the same goes for a file replaced by rename.
//
// The same rule as the OTLP headers file (OTEL_EXPORTER_OTLP_HEADERS_FILE):
//   - the plain variable stays supported, and is the fallback when the file
//     cannot be used;
//   - a missing, unreadable or empty file never takes the app down: the last
//     token read from it keeps being used, and one WARN per minute says why.

// tokenFileWarnEvery bounds how often a persistently unusable file is logged.
var tokenFileWarnEvery = time.Minute

type tokenFile struct {
	path string
	log  *slog.Logger

	mu       sync.Mutex
	last     string // last non-empty token read
	lastWarn time.Time
	failing  bool
}

var (
	tokenFilesMu sync.Mutex
	tokenFiles   = map[string]*tokenFile{}
)

func sharedTokenFile(path string) *tokenFile {
	tokenFilesMu.Lock()
	defer tokenFilesMu.Unlock()
	f, ok := tokenFiles[path]
	if !ok {
		f = &tokenFile{path: path, log: slog.Default().With("component", "idp-token-file")}
		tokenFiles[path] = f
	}
	return f
}

// read returns the file's current token, or the last good one when the file is
// unusable now. ok is false only when no token has ever been read.
func (f *tokenFile) read() (token string, ok bool) {
	raw, err := os.ReadFile(f.path)
	tok := strings.TrimSpace(string(raw))

	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil && tok != "" {
		if f.failing {
			f.log.Info("IDP broker token file is readable again", "path", f.path)
		}
		f.failing = false
		f.last = tok
		return tok, true
	}
	f.failing = true
	if f.lastWarn.IsZero() || time.Since(f.lastWarn) >= tokenFileWarnEvery {
		f.lastWarn = time.Now()
		reason := "file is empty"
		if err != nil {
			reason = err.Error()
		}
		f.log.Warn("IDP broker token file unusable; using the last token read from it, or IDP_BROKER_TOKEN",
			"path", f.path, "reason", reason, "have_last", f.last != "")
	}
	return f.last, f.last != ""
}

// bearer is the token to present now: the file's when Client.TokenFile is set
// and has ever yielded one, else Client.Token.
func (c *Client) bearer() string {
	if c.TokenFile != "" {
		if t, ok := sharedTokenFile(c.TokenFile).read(); ok {
			return t
		}
	}
	return c.Token
}

// HasBrokerToken reports whether a token is available from either source.
// Use it for a boot-time "is sign-in configured" check; it reads the file.
func (c *Client) HasBrokerToken() bool { return c.bearer() != "" }
