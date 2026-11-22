// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/selfupdate"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/cliupdate"
)

// releaseServer is a fake downloads.reliantlabs.io: the channel manifest
// naming one release on latest, and that release's archive for this
// platform, checksums.txt and signature bundle. The archive's reliant is a
// script that exits 0, so the staged binary passes its `version` probe.
type releaseServer struct {
	srv     *httptest.Server
	archive string
	mu      sync.Mutex
	fetched map[string]int
}

func newReleaseServer(t *testing.T, tag string) *releaseServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake release's binary is a shell script")
	}
	rs := &releaseServer{archive: fmt.Sprintf("reliant_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), fetched: map[string]int{}}
	archive := tarGz(t, "reliant", "#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + rs.archive + "\n"
	rs.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.fetched[r.URL.Path]++
		rs.mu.Unlock()
		base := rs.srv.URL + "/cli/" + tag + "/"
		switch r.URL.Path {
		case "/daemon-channels.json":
			fmt.Fprintf(w, `{"schema": 1, "repository": "reliant-labs/reliant", "channels": {"latest": {
				"version": %q, "tag": %q, "commit": "c", "commit_time_unix": 1795270304,
				"checksums": {"url": "%schecksums.txt"}, "bundle": {"url": "%sreliant-release.sigstore.json"},
				"platforms": {"%s/%s": {"url": "%s%s"}}}}}`,
				strings.TrimPrefix(tag, "v"), tag, base, base, runtime.GOOS, runtime.GOARCH, base, rs.archive)
		case "/cli/" + tag + "/checksums.txt":
			_, _ = io.WriteString(w, checksums)
		case "/cli/" + tag + "/reliant-release.sigstore.json":
			_, _ = io.WriteString(w, `{"fake": "bundle"}`)
		case "/cli/" + tag + "/" + rs.archive:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *releaseServer) count(name string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n := 0
	for p, c := range rs.fetched {
		if strings.HasSuffix(p, "/"+name) {
			n += c
		}
	}
	return n
}

func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := io.WriteString(tw, body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// recordingVerifier stands in for the Sigstore verifier: it records which
// files it was handed and answers with err.
type recordingVerifier struct {
	mu    sync.Mutex
	files []string
	tag   string
	err   error
}

func (v *recordingVerifier) Verify(_ context.Context, rel selfupdate.Release, files map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.tag = rel.Tag
	for name := range files {
		v.files = append(v.files, name)
	}
	return v.err
}

// swapRecorder stands in for the swap (which would replace the test binary).
type swapRecorder struct {
	called bool
	staged selfupdate.Staged
	argv   []string
}

func (s *swapRecorder) apply(_ context.Context, _ *selfupdate.Updater, staged selfupdate.Staged, argv []string) error {
	s.called, s.staged, s.argv = true, staged, argv
	return nil
}

func selfOwned() (selfupdate.Install, error) {
	return selfupdate.Install{Path: "/home/me/.local/bin/reliant", Kind: selfupdate.SelfOwned, Reason: "no owner detected"}, nil
}

func newTestUpdateRun(t *testing.T, rs *releaseServer, current string, v selfupdate.Verifier) (*updateRun, *bytes.Buffer, *swapRecorder) {
	t.Helper()
	var out bytes.Buffer
	swap := &swapRecorder{}
	return &updateRun{
		out: &out,
		in:  strings.NewReader(""),
		opts: cliupdate.Options{
			StateDir:    t.TempDir(),
			Current:     selfupdate.Build{Version: current},
			ManifestURL: rs.srv.URL + "/daemon-channels.json",
			HTTPClient:  rs.srv.Client(),
			Verifier:    v,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		goos:   runtime.GOOS,
		detect: selfOwned,
		apply:  swap.apply,
	}, &out, swap
}

func TestUpdateCheckReportsAnAvailableRelease(t *testing.T) {
	rs := newReleaseServer(t, "v1.26.0")
	u, out, swap := newTestUpdateRun(t, rs, "v1.25.0", &recordingVerifier{})
	u.check, u.json = true, true
	require.NoError(t, u.run(context.Background()))

	var report updateReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.True(t, report.UpdateAvailable)
	require.Equal(t, "v1.25.0", report.Current.Version)
	require.Equal(t, cliupdate.ChannelLatest, report.Channel)
	require.Equal(t, "v1.26.0", report.Release.Tag)
	require.Equal(t, int64(1795270304), report.Release.CommitTimeUnix)
	require.True(t, report.Install.SelfUpdatable)
	require.Equal(t, "reliant update", report.Install.Advice)
	require.Zero(t, rs.count(rs.archive), "--check downloads nothing")
	require.False(t, swap.called)
}

func TestUpdateWhenAlreadyCurrentDoesNothing(t *testing.T) {
	rs := newReleaseServer(t, "v1.26.0")
	u, out, swap := newTestUpdateRun(t, rs, "v1.26.0", &recordingVerifier{})
	u.yes = true
	require.NoError(t, u.run(context.Background()))
	require.Contains(t, out.String(), "Up to date")
	require.Zero(t, rs.count(rs.archive))
	require.False(t, swap.called)
}

// An install something else owns is never replaced, and the command says
// what does update it.
func TestUpdateRefusesAnInstallItDoesNotOwn(t *testing.T) {
	for _, in := range []selfupdate.Install{
		{Path: "/Applications/Reliant.app/Contents/Resources/server/mac-arm64/reliant-backend", Kind: selfupdate.Bundle, Owner: "app bundle", Reason: "inside a macOS .app bundle"},
		{Path: "/opt/homebrew/Cellar/reliant/1.25.0/bin/reliant", Kind: selfupdate.PackageManager, Owner: "homebrew", Hint: "brew upgrade reliant", Reason: "installed under Homebrew's Cellar"},
		{Path: "/usr/local/bin/reliant", Kind: selfupdate.ReadOnly, Reason: "cannot write /usr/local/bin"},
	} {
		t.Run(in.Kind.String(), func(t *testing.T) {
			rs := newReleaseServer(t, "v1.26.0")
			u, out, swap := newTestUpdateRun(t, rs, "v1.25.0", &recordingVerifier{})
			u.yes = true
			u.detect = func() (selfupdate.Install, error) { return in, nil }
			err := u.run(context.Background())
			require.Error(t, err)
			require.Contains(t, err.Error(), cliupdate.Advice(in))
			require.Zero(t, rs.count(rs.archive), "nothing is downloaded for an install that cannot take it")
			require.False(t, swap.called)

			// --check reports the same, without failing.
			out.Reset()
			u.check, u.yes = true, false
			require.NoError(t, u.run(context.Background()))
			require.Contains(t, out.String(), cliupdate.Advice(in))
		})
	}
}

// The release is downloaded, its files handed to the verifier, the archive's
// binary staged and probed, and only then the swap runs, re-executing the new
// binary to confirm itself.
func TestUpdateVerifiesStagesThenSwaps(t *testing.T) {
	rs := newReleaseServer(t, "v1.26.0")
	v := &recordingVerifier{}
	u, out, swap := newTestUpdateRun(t, rs, "v1.25.0", v)
	u.yes = true
	require.NoError(t, u.run(context.Background()))

	require.Equal(t, "v1.26.0", v.tag)
	require.ElementsMatch(t, []string{rs.archive, "checksums.txt", cliupdate.SignatureAsset}, v.files)
	require.True(t, swap.called)
	require.FileExists(t, swap.staged.Path)
	require.Equal(t, "1.26.0", swap.staged.Release.Version)
	require.Equal(t, []string{"update", "--swapped", "--from", "v1.25.0"}, swap.argv[1:])
	require.Contains(t, out.String(), "Signature verified")
}

// A release whose signature does not verify is not staged, and nothing is
// replaced.
func TestUpdateRefusesAnUnverifiedRelease(t *testing.T) {
	rs := newReleaseServer(t, "v1.26.0")
	u, _, swap := newTestUpdateRun(t, rs, "v1.25.0", &recordingVerifier{err: errors.New("certificate identity does not match")})
	u.yes = true
	err := u.run(context.Background())
	require.ErrorContains(t, err, "nothing was replaced")
	require.ErrorContains(t, err, "certificate identity does not match")
	require.False(t, swap.called)
}

func TestUpdateAsksBeforeReplacing(t *testing.T) {
	rs := newReleaseServer(t, "v1.26.0")

	u, _, swap := newTestUpdateRun(t, rs, "v1.25.0", &recordingVerifier{})
	err := u.run(context.Background())
	require.ErrorContains(t, err, "without --yes")
	require.False(t, swap.called)

	u, out, swap := newTestUpdateRun(t, rs, "v1.25.0", &recordingVerifier{})
	u.interactive, u.in = true, strings.NewReader("n\n")
	require.ErrorContains(t, u.run(context.Background()), "cancelled")
	require.Contains(t, out.String(), "Update reliant v1.25.0 to 1.26.0")
	require.False(t, swap.called)
	require.Zero(t, rs.count(rs.archive))

	u, _, swap = newTestUpdateRun(t, rs, "v1.25.0", &recordingVerifier{})
	u.interactive, u.in = true, strings.NewReader("y\n")
	require.NoError(t, u.run(context.Background()))
	require.True(t, swap.called)
}

// Windows cannot replace a running .exe yet: the update is reported, not
// attempted.
func TestUpdateOnWindowsDoesNotSwap(t *testing.T) {
	rs := newReleaseServer(t, "v1.26.0")
	u, _, swap := newTestUpdateRun(t, rs, "v1.25.0", &recordingVerifier{})
	u.yes, u.goos = true, "windows"
	require.ErrorContains(t, u.run(context.Background()), "on Windows yet")
	require.Zero(t, rs.count(rs.archive))
	require.False(t, swap.called)
}

func TestUpdateFlags(t *testing.T) {
	for args, want := range map[string]string{
		"--json":              "--json needs --check or --yes",
		"--check --channel x": `--channel "x"`,
	} {
		cmd := newUpdateCmd()
		cmd.SetArgs(strings.Fields(args))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		require.ErrorContains(t, cmd.Execute(), want, args)
	}
}

// The swapped binary's run confirms the swap even when no swap is recorded
// (it is then a no-op), and names both versions.
func TestFinishSwapReports(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, finishSwap(&out, cliupdate.Options{StateDir: t.TempDir()}, "v1.25.0", false))
	require.Contains(t, out.String(), "Updated reliant v1.25.0 to ")
}
