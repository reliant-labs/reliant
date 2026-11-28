// Copyright (c) 2026 Reliant Labs
package cliupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"
	"github.com/stretchr/testify/require"
)

// fakeRelease serves a manifest naming tag on latest, and that release's
// archive (a reliant that exits 0, so the staged binary passes its probe),
// checksums.txt and bundle. archives counts archive downloads.
func fakeRelease(t *testing.T, tag string) (manifestURL string, client *http.Client, archives *atomic.Int64) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake release's binary is a shell script")
	}
	archiveName := fmt.Sprintf("reliant_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\nexit 0\n"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "reliant", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, _ = io.WriteString(tw, body)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	archives = &atomic.Int64{}
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := srv.URL + "/cli/" + tag + "/"
		switch {
		case r.URL.Path == "/daemon-channels.json":
			fmt.Fprintf(w, `{"schema": 1, "repository": "reliant-labs/reliant", "channels": {"latest": {
				"version": %q, "tag": %q, "commit": "c", "commit_time_unix": 1795270304,
				"checksums": {"url": "%schecksums.txt"}, "bundle": {"url": "%sreliant-release.sigstore.json"},
				"platforms": {"%s/%s": {"url": "%s%s"}}}}}`,
				strings.TrimPrefix(tag, "v"), tag, base, base, runtime.GOOS, runtime.GOARCH, base, archiveName)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), archiveName)
		case strings.HasSuffix(r.URL.Path, "/"+SignatureAsset):
			_, _ = io.WriteString(w, "{}")
		case strings.HasSuffix(r.URL.Path, "/"+archiveName):
			archives.Add(1)
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/daemon-channels.json", srv.Client(), archives
}

func selfOwnedInstall(func() (string, bool)) (selfupdate.Install, error) {
	return selfupdate.Install{Path: "/home/me/.local/bin/reliant", Kind: selfupdate.SelfOwned}, nil
}

func testDaemon(t *testing.T, current string, auto bool, detect func(func() (string, bool)) (selfupdate.Install, error)) (*Daemon, *atomic.Int64) {
	t.Helper()
	url, client, archives := fakeRelease(t, "v1.27.0")
	opts := DaemonOptions{AutoUpdate: auto, Detect: detect, GOOS: "linux"}
	opts.StateDir = t.TempDir()
	opts.Current = selfupdate.Build{Version: current}
	opts.ManifestURL, opts.HTTPClient = url, client
	opts.Verifier = fakeVerifier{}
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := NewDaemon(opts)
	require.NoError(t, err)
	return d, archives
}

// Only an opted-in daemon whose install nothing else owns, on a platform
// that can swap, updates itself; the others say why and how instead.
func TestDaemonAutoUpdateNeedsOptInAndASelfOwnedInstall(t *testing.T) {
	d, _ := testDaemon(t, "v1.26.0", true, selfOwnedInstall)
	require.True(t, d.AutoUpdate())
	require.Empty(t, d.WhyNotAuto())

	d, _ = testDaemon(t, "v1.26.0", false, selfOwnedInstall)
	require.False(t, d.AutoUpdate())
	require.Contains(t, d.WhyNotAuto(), "--auto-update")
	require.True(t, d.SelfOwned())
	require.ErrorIs(t, func() error { _, err := d.Stage(context.Background()); return err }(), ErrAutoUpdateOff)

	bundle := func(func() (string, bool)) (selfupdate.Install, error) {
		return selfupdate.Install{Path: "/opt/Reliant/resources/server/linux-amd64/reliant-backend", Kind: selfupdate.Bundle, Reason: "inside an Electron app's resources"}, nil
	}
	d, _ = testDaemon(t, "v1.26.0", true, bundle)
	require.False(t, d.AutoUpdate())
	require.Contains(t, d.WhyNotAuto(), "inside an Electron app's resources")
	require.Contains(t, d.Advice(), "desktop app")

	d, _ = testDaemon(t, "v1.26.0", true, selfOwnedInstall)
	d.goos = "windows"
	require.False(t, d.AutoUpdate())
	require.Contains(t, d.WhyNotAuto(), "Windows")

	d, _ = testDaemon(t, "v1.26.0", true, selfOwnedInstall)
	d.Disable(errors.New("disk full"))
	require.False(t, d.AutoUpdate())
	require.Contains(t, d.WhyNotAuto(), "disk full")
}

// Stage downloads and verifies a newer release once, and reports when there
// is none.
func TestDaemonStagesANewerReleaseOnce(t *testing.T) {
	plainHost(t) // the Updater detects the running test binary again when it stages
	d, archives := testDaemon(t, "v1.26.0", true, selfOwnedInstall)
	v, err := d.Stage(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1.27.0", v)
	v, err = d.Stage(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1.27.0", v)
	require.EqualValues(t, 1, archives.Load(), "a staged release is not downloaded again")

	d, archives = testDaemon(t, "v1.27.0", true, selfOwnedInstall)
	_, err = d.Stage(context.Background())
	require.ErrorIs(t, err, ErrNoUpdate)
	require.Zero(t, archives.Load())
}

// Apply drains through the runtime and asks it to shut down, arming the swap;
// a drain that declines leaves nothing armed and nothing shut down.
func TestDaemonApplyDrainsThenShutsDown(t *testing.T) {
	plainHost(t) // the Updater detects the running test binary again when it stages
	d, _ := testDaemon(t, "v1.26.0", true, selfOwnedInstall)
	require.Error(t, d.Apply(context.Background(), ApplyRequest{}), "nothing staged")
	_, err := d.Stage(context.Background())
	require.NoError(t, err)

	// A fixed deadline: the fake drains never read it, and no test reads
	// the wall clock.
	deadline := time.Unix(1_800_000_060, 0)
	var shutdowns atomic.Int64
	declined := errors.New("still running")
	err = d.Apply(context.Background(), ApplyRequest{
		Deadline: deadline,
		Drain:    func(context.Context, time.Time) (int, error) { return 1, declined },
		Shutdown: func() { shutdowns.Add(1) },
	})
	require.ErrorIs(t, err, declined)
	require.False(t, d.Armed())
	require.Zero(t, shutdowns.Load())

	var drained atomic.Int64
	require.NoError(t, d.Apply(context.Background(), ApplyRequest{
		Urgency:  selfupdate.Routine,
		Deadline: deadline,
		Drain:    func(context.Context, time.Time) (int, error) { drained.Add(1); return 0, nil },
		Shutdown: func() { shutdowns.Add(1) },
	}))
	require.EqualValues(t, 1, drained.Load())
	require.EqualValues(t, 1, shutdowns.Load())
	require.True(t, d.Armed(), "the swap waits for ExecPending after the runtime's teardown")
}

// A daemon with no swap in flight confirms nothing, harmlessly.
func TestDaemonConfirmWithoutASwap(t *testing.T) {
	d, _ := testDaemon(t, "v1.26.0", false, selfOwnedInstall)
	d.Confirm()
	d.Confirm()
	v, why := d.Reverted()
	require.Empty(t, v)
	require.Empty(t, why)
}
