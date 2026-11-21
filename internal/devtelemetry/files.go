package devtelemetry

// The credential files a hosted dev stack exports with, and the small state
// beside them that `forge env status` reads.
//
// Every file here is under the project's .forge/obs/, mode 0600, and replaced
// by an atomic rename: pkg/observe re-reads the headers file on each export
// and the dev server's /_otel proxy on each request, so a reader must never
// see a half-written token.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// HeadersPath is the process token's headers file, in the
// `authorization=Bearer <token>` form OTEL_EXPORTER_OTLP_HEADERS_FILE names.
func HeadersPath(projectDir string) string {
	return filepath.Join(projectDir, ".forge", "obs", "otlp-headers")
}

// BrowserHeadersPath is the browser token's headers file, read by the
// frontend dev server's /_otel proxy (FORGE_OTEL_HEADERS_FILE).
func BrowserHeadersPath(projectDir string) string {
	return filepath.Join(projectDir, ".forge", "obs", "otlp-headers-browser")
}

// NoticePath records that the first-run notice of hosted dev telemetry was
// shown in this project.
func NoticePath(projectDir string) string {
	return filepath.Join(projectDir, ".forge", "obs", "hosted-notice-shown")
}

// StatePath is env's token state: when the current tokens expire, the
// retention the control plane granted, and why the last renewal failed.
func StatePath(projectDir, env string) string {
	return filepath.Join(projectDir, ".forge", "obs", "telemetry-"+env+".json")
}

// Retention is the effective retention the control plane granted, in days.
type Retention struct {
	TracesDays  int `json:"traces_days,omitempty"`
	MetricsDays int `json:"metrics_days,omitempty"`
	LogsDays    int `json:"logs_days,omitempty"`
	BrowserDays int `json:"browser_days,omitempty"`
}

// State is .forge/obs/telemetry-<env>.json.
type State struct {
	Env       string    `json:"env"`
	ExpiresAt time.Time `json:"expires_at"`
	RenewedAt time.Time `json:"renewed_at"`
	Retention Retention `json:"retention"`
	// RenewError is why the last renewal failed; empty after a success.
	RenewError string `json:"renew_error,omitempty"`
}

// WriteHeaders replaces both headers files with the two tokens.
func WriteHeaders(projectDir, processToken, browserToken string) error {
	if err := writeAtomic(HeadersPath(projectDir), []byte("authorization=Bearer "+processToken+"\n")); err != nil {
		return err
	}
	return writeAtomic(BrowserHeadersPath(projectDir), []byte("authorization=Bearer "+browserToken+"\n"))
}

// ReadState returns env's token state; ok is false when none was written.
func ReadState(projectDir, env string) (st State, ok bool, err error) {
	b, err := os.ReadFile(StatePath(projectDir, env))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, false, err
	}
	return st, true, nil
}

// WriteState replaces env's token state atomically.
func WriteState(projectDir string, st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(StatePath(projectDir, st.Env), append(b, '\n'))
}

// MarkNoticeShown records that the first-run notice was shown.
func MarkNoticeShown(projectDir string, at time.Time) error {
	return writeAtomic(NoticePath(projectDir), []byte(at.UTC().Format(time.RFC3339)+"\n"))
}

// NoticeShown reports whether the first-run notice was already shown.
func NoticeShown(projectDir string) bool {
	_, err := os.Stat(NoticePath(projectDir))
	return err == nil
}

// writeAtomic writes p (0600) through a sibling temp file and a rename. The
// directory gets its own .gitignore, so a project whose .gitignore does not
// cover .forge/ still never commits a token.
func writeAtomic(p string, b []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := atomicfile.WriteFile(ignore, []byte("*\n"), 0o600); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, p)
}

// ClearCredentials removes env's token state, and the headers files unless
// another env of this project is recorded as shipping to the hosted store
// (the headers files are per project). Called whenever env's telemetry is
// decided local, so a local stack never keeps a usable hosted token on disk
// and `forge env status` never reads a stale grant. A missing file is not an
// error.
func ClearCredentials(projectDir, env string) error {
	var errs []error
	rm := func(p string) {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	rm(StatePath(projectDir, env))
	if !otherEnvHosted(projectDir, env) {
		rm(HeadersPath(projectDir))
		rm(BrowserHeadersPath(projectDir))
	}
	return errors.Join(errs...)
}

// otherEnvHosted reports whether an env other than env is recorded as
// shipping to the hosted store.
func otherEnvHosted(projectDir, env string) bool {
	matches, _ := filepath.Glob(filepath.Join(projectDir, ".forge", "obs", "backend-*.json"))
	for _, m := range matches {
		other := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "backend-"), ".json")
		if other == env {
			continue
		}
		if rec, ok, err := Read(projectDir, other); err == nil && ok && rec.Backend == "cloud" {
			return true
		}
	}
	return false
}
