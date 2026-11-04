package hostinfra

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Every platform forge supports has a pinned checksum for both engines, so
// "no binary for this platform" can only mean upstream stopped publishing it.
func TestSupervisedReleasesPinEveryPlatform(t *testing.T) {
	for _, rel := range []binaryRelease{natsRelease, temporalRelease} {
		for _, goos := range []string{"linux", "darwin", "windows"} {
			for _, goarch := range []string{"amd64", "arm64"} {
				asset, ok := rel.assetFor(goos, goarch)
				if !ok {
					t.Errorf("%s: no checksum for %s (%s/%s)", rel.engine, asset, goos, goarch)
				}
				if sum := rel.checksums[asset]; len(sum) != 64 {
					t.Errorf("%s: checksum for %s is %q, not a sha256", rel.engine, asset, sum)
				}
			}
		}
		if got := len(rel.checksums); got != 6 {
			t.Errorf("%s: %d checksums, want exactly the 6 platforms (a stale entry is a pin that drifted)", rel.engine, got)
		}
	}
}

func TestSupervisedBinaryPathIsVersioned(t *testing.T) {
	p := natsRelease.binaryPathFor("linux", "amd64")
	want := filepath.Join("forge", "nats", NATSVersion, "linux-amd64", "nats-server")
	if !bytes.HasSuffix([]byte(p), []byte(want)) {
		t.Fatalf("binary path %q does not end in %q", p, want)
	}
	if got := temporalRelease.binaryPathFor("windows", "arm64"); filepath.Base(got) != "temporal.exe" {
		t.Fatalf("windows binary is %q, want temporal.exe", filepath.Base(got))
	}
}

func TestExtractMemberFlattensTarAndZip(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("#!/bin/sh\necho hi\n")

	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"pkg/LICENSE", []byte("x")}, {"pkg/nats-server", payload}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	tarPath := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(tarPath, tgz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("pkg/nats-server.exe")
	_, _ = w.Write(payload)
	_ = zw.Close()
	zipPath := filepath.Join(dir, "a.zip")
	if err := os.WriteFile(zipPath, zb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ archive, asset, member string }{
		{tarPath, "nats-server-x.tar.gz", "nats-server"},
		{zipPath, "nats-server-x.zip", "nats-server.exe"},
	} {
		dest := filepath.Join(dir, "out-"+tc.member)
		if err := extractMember(tc.archive, tc.asset, tc.member, dest); err != nil {
			t.Fatalf("%s: %v", tc.asset, err)
		}
		got, err := os.ReadFile(dest)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s: extracted %q, %v", tc.asset, got, err)
		}
		if info, _ := os.Stat(dest); runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
			t.Fatalf("%s: extracted binary is not executable", tc.asset)
		}
	}
	if err := extractMember(tarPath, "x.tar.gz", "temporal", filepath.Join(dir, "none")); err == nil {
		t.Fatal("extracting a member the archive lacks succeeded")
	}
}

func TestPIDFileRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.pid")
	if err := writePIDFile(p, 4242, 21050); err != nil {
		t.Fatal(err)
	}
	pid, port, ok := readPIDFile(p)
	if !ok || pid != 4242 || port != 21050 {
		t.Fatalf("read back %d %d %v", pid, port, ok)
	}
	if err := os.WriteFile(p, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readPIDFile(p); ok {
		t.Fatal("a malformed pidfile read as valid")
	}
}

func TestNATSArgs(t *testing.T) {
	spec := Spec{Name: "nats", Port: 21050}
	args := natsArgs(spec, "/d")
	for _, want := range []string{"--jetstream", "127.0.0.1", "21050", filepath.Join("/d", "jetstream")} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v lack %q", args, want)
		}
	}
	if slices.Contains(args, "--http_port") {
		t.Errorf("no monitor port declared, yet args %v enable monitoring", args)
	}
	spec.NATSMonitorPort = 21051
	if args := natsArgs(spec, "/d"); !slices.Contains(args, "21051") {
		t.Errorf("monitor port missing from %v", args)
	}
}

func TestNATSInfoReady(t *testing.T) {
	for line, want := range map[string]bool{
		`INFO {"server_id":"x","jetstream":true}` + "\r\n": true,
		`INFO {"server_id":"x"}` + "\r\n":                  false,
		`INFO {"jetstream":false}`:                         false,
		"-ERR 'nope'\r\n":                                  false,
		"SSH-2.0-OpenSSH\r\n":                              false,
	} {
		if got := natsInfoReady(line); got != want {
			t.Errorf("natsInfoReady(%q) = %v, want %v", line, got, want)
		}
	}
}

// The probe reads what a real server sends on connect, and a listener that
// says nothing (any non-NATS service) is "not ready", promptly.
func TestNATSReadyProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = fmt.Fprint(c, `INFO {"jetstream":true}`+"\r\n")
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	if !natsReady(context.Background(), port) {
		t.Fatal("a server announcing JetStream read as not ready")
	}

	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	start := time.Now()
	if natsReady(context.Background(), silent.Addr().(*net.TCPAddr).Port) {
		t.Fatal("a silent listener read as ready")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("probing a silent listener took %s", d)
	}
}

func TestTemporalArgs(t *testing.T) {
	spec := Spec{Name: "temporal", Port: 21060, TemporalNamespaces: []string{"reliant"}}
	args := temporalArgs(spec, "/d")
	for _, want := range []string{"start-dev", "21060", filepath.Join("/d", "temporal.db"), "--headless", "reliant", "127.0.0.1"} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v lack %q", args, want)
		}
	}
	spec.TemporalUIPort = 21062
	args = temporalArgs(spec, "/d")
	if slices.Contains(args, "--headless") || !slices.Contains(args, "21062") {
		t.Errorf("a declared UI port should replace --headless: %v", args)
	}
	if got := temporalNamespaces(Spec{}); !slices.Equal(got, []string{"default"}) {
		t.Errorf("no declared namespace should wait on default, got %v", got)
	}
}

func TestSupervisedVersionMustMatchThePin(t *testing.T) {
	err := natsEngine.start(context.Background(), t.TempDir(), Spec{Name: "nats", Engine: EngineNATS, Port: 1, Version: "v2.0.0"})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte(NATSVersion)) {
		t.Fatalf("a mismatched version was not refused naming the pin: %v", err)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// The real thing, end to end: fetch (or reuse the cache), start, report
// ready, restart idempotently, stop. Slow — a first run downloads ~60 MB.
func TestSupervisedEnginesStartAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads and runs nats-server and the Temporal dev server; runs in task test")
	}
	for _, tc := range []struct {
		engine string
		spec   func(port int) Spec
	}{
		{EngineNATS, func(port int) Spec { return Spec{Name: "nats", Engine: EngineNATS, Port: port} }},
		{EngineTemporal, func(port int) Spec {
			return Spec{Name: "temporal", Engine: EngineTemporal, Port: port, TemporalNamespaces: []string{"forge-test"}}
		}},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			project := t.TempDir()
			spec := tc.spec(freeTCPPort(t))
			if err := Start(ctx, project, spec); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _, _ = StopReport(project, spec) })
			st, err := StatusOf(ctx, project, spec)
			if err != nil || !st.Running || !st.Ready || st.Port != spec.Port {
				t.Fatalf("status after start: %+v, %v", st, err)
			}
			if err := Start(ctx, project, spec); err != nil {
				t.Fatalf("second start was not idempotent: %v", err)
			}
			stopped, err := StopReport(project, spec)
			if err != nil || !stopped {
				t.Fatalf("stop: stopped=%v err=%v", stopped, err)
			}
			if portListening(spec.Port) {
				t.Fatalf("port %s still held after stop", strconv.Itoa(spec.Port))
			}
			if stopped, _ := StopReport(project, spec); stopped {
				t.Fatal("a second stop reported stopping something")
			}
		})
	}
}

// HostInfra.version is a postgres MAJOR, and embedded-postgres fetches by
// full release: a bare "16" used to ask Maven for an artifact that does not
// exist (embedded-postgres-binaries-linux-amd64/16 → 404).
func TestPostgresVersionResolvesAMajor(t *testing.T) {
	for in, want := range map[string]string{"16": "16.9.0", "18": "18.3.0", "16.4.0": "16.4.0"} {
		if got := string(postgresRelease(in)); got != want {
			t.Errorf("postgresRelease(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"16": "16", "16.9.0": "16", "17.5": "17"} {
		if got := PostgresMajor(in); got != want {
			t.Errorf("PostgresMajor(%q) = %q, want %q", in, got, want)
		}
	}
}
