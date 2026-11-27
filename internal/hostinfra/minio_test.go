package hostinfra

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

// productionCredentials returns root credentials of the sizes render has
// FileSecrets generate (kcl/render.k _render_secret_provider): 16 random
// bytes in hex for the user, 32 for the password.
func productionCredentials(t *testing.T) (user, password string) {
	t.Helper()
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:16]), hex.EncodeToString(b[16:])
}

// nonLoopbackIPv4 is one of this host's addresses that is not loopback, ""
// when it has none.
func nonLoopbackIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

func minioSpec(port int) Spec {
	return Spec{
		Name: "minio", Env: "dev", Engine: EngineMinIO, Port: port,
		MinIORootUser: "forge-test-user", MinIORootPassword: "forge-test-password-0123456789",
	}
}

// MinIO publishes one bare binary per platform, and none for windows/arm64.
func TestMinIOReleasePinsEveryPublishedPlatform(t *testing.T) {
	for _, p := range []struct{ goos, goarch string }{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		asset, ok := minioRelease.assetFor(p.goos, p.goarch)
		if !ok {
			t.Errorf("no checksum for %s (%s/%s)", asset, p.goos, p.goarch)
		}
		if sum := minioRelease.checksums[asset]; len(sum) != 64 {
			t.Errorf("checksum for %s is %q, not a sha256", asset, sum)
		}
	}
	if asset, ok := minioRelease.assetFor("windows", "arm64"); ok {
		t.Errorf("%s has a checksum, but upstream publishes no windows/arm64 build", asset)
	}
	if got := len(minioRelease.checksums); got != 5 {
		t.Errorf("%d checksums, want exactly the 5 published platforms (a stale entry is a pin that drifted)", got)
	}
	if got := minioRelease.asset("windows", "amd64"); !strings.HasSuffix(got, MinIOVersion+".exe") {
		t.Errorf("windows asset %q", got)
	}
	if !minioRelease.raw {
		t.Error("MinIO's release asset is the binary itself, not an archive")
	}
}

// A raw release is checksum-verified as downloaded and installed by rename,
// executable, with nothing extracted.
func TestRawReleaseDownloadVerifiesAndInstalls(t *testing.T) {
	payload := []byte("#!/bin/sh\necho 'fake version v9'\n")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer srv.Close()
	rel := binaryRelease{
		engine:    "fake",
		version:   "v9",
		checksums: map[string]string{"fake-bin": hex.EncodeToString(sum[:])},
		asset:     func(_, _ string) string { return "fake-bin" },
		url:       func(asset string) string { return srv.URL + "/" + asset },
		binary:    func(string) string { return "fake" },
		raw:       true,
	}
	dest := filepath.Join(t.TempDir(), "bin", "fake")
	if err := rel.download(context.Background(), "fake-bin", dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("installed %q, %v", got, err)
	}
	if info, _ := os.Stat(dest); runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Fatal("installed binary is not executable")
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Fatalf("staging files left beside the binary: %v", entries)
	}

	rel.checksums["fake-bin"] = strings.Repeat("0", 64)
	if err := rel.download(context.Background(), "fake-bin", filepath.Join(t.TempDir(), "other")); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a substituted binary was not refused: %v", err)
	}
}

func TestMinIOArgsCarryNoCredential(t *testing.T) {
	spec := minioSpec(9100)
	args := minioArgs(spec, "/d")
	for _, want := range []string{"server", filepath.Join("/d", "data"), "127.0.0.1:9100", filepath.Join("/d", "config"), filepath.Join("/d", "certs")} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v lack %q", args, want)
		}
	}
	if slices.Contains(args, "--console-address") {
		t.Errorf("a console was asked for with no console port: %v", args)
	}
	for _, a := range args {
		if strings.Contains(a, spec.MinIORootUser) || strings.Contains(a, spec.MinIORootPassword) {
			t.Fatalf("a credential is in argv, which every user can read: %v", args)
		}
	}
	spec.MinIOConsolePort = 9101
	if args := minioArgs(spec, "/d"); !slices.Contains(args, "127.0.0.1:9101") {
		t.Errorf("console port not passed: %v", args)
	}
}

func TestMinIOEnvDropsInheritedMinIOVariables(t *testing.T) {
	spec := minioSpec(9100)
	spec.MinIOCORSAllowOrigin = "http://localhost:3000"
	env := minioEnv(spec, []string{"PATH=/bin", "MINIO_ROOT_USER=someone-else", "MINIO_VOLUMES=/elsewhere"})
	for _, want := range []string{
		"PATH=/bin",
		"MINIO_ROOT_USER=forge-test-user",
		"MINIO_ROOT_PASSWORD=forge-test-password-0123456789",
		"MINIO_SITE_REGION=us-east-1",
		"MINIO_API_CORS_ALLOW_ORIGIN=http://localhost:3000",
		"MINIO_UPDATE=off",
		"MINIO_BROWSER=off",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %q: %v", want, env)
		}
	}
	for _, kv := range env {
		if kv == "MINIO_ROOT_USER=someone-else" || strings.HasPrefix(kv, "MINIO_VOLUMES=") {
			t.Errorf("an inherited MINIO_* variable survived: %q", kv)
		}
	}
	for _, goos := range []string{"linux", "windows"} {
		for _, kv := range minioEnvFor(goos, spec, []string{"_MINIO_SERVER_DEBUG=on", "MINIO_OPTS=--x"}) {
			if strings.HasPrefix(kv, "_MINIO_") || strings.HasPrefix(kv, "MINIO_OPTS") {
				t.Errorf("%s: an inherited server variable survived: %q", goos, kv)
			}
		}
	}
	// Windows names are case-insensitive: a lower-case minio_root_user IS
	// MINIO_ROOT_USER there, and must go. Elsewhere it is another variable.
	lower := []string{"minio_root_user=someone-else", "_minio_ci_cd=on"}
	if got := minioEnvFor("windows", spec, lower); slices.Contains(got, lower[0]) || slices.Contains(got, lower[1]) {
		t.Errorf("windows kept a lower-case MinIO variable: %v", got)
	}
	if got := minioEnvFor("linux", spec, lower); !slices.Contains(got, lower[0]) {
		t.Errorf("linux dropped %q, which is not a variable MinIO reads there: %v", lower[0], got)
	}

	spec.MinIOConsolePort = 9101
	spec.MinIOCORSAllowOrigin = ""
	spec.MinIORegion = "eu-west-1"
	env = minioEnv(spec, nil)
	if slices.Contains(env, "MINIO_BROWSER=off") {
		t.Error("the console was switched off although a console port is declared")
	}
	if !slices.Contains(env, "MINIO_API_CORS_ALLOW_ORIGIN=*") || !slices.Contains(env, "MINIO_SITE_REGION=eu-west-1") {
		t.Errorf("defaults/region not applied: %v", env)
	}
}

func TestValidateMinIONamesTheStoreForMissingCredentials(t *testing.T) {
	spec := minioSpec(9100)
	spec.MinIORootPassword = ""
	if err := validateMinIO(spec); err == nil || !strings.Contains(err.Error(), "forge secret ensure --env dev") {
		t.Errorf("missing credential: %v", err)
	}
	spec = minioSpec(9100)
	spec.MinIORootPassword = "short"
	if err := validateMinIO(spec); err == nil || !strings.Contains(err.Error(), "at least 8") {
		t.Errorf("short password: %v", err)
	}
	spec = minioSpec(9100)
	spec.MinIOConsolePort = 9100
	if err := validateMinIO(spec); err == nil {
		t.Error("a console on the API's port was accepted")
	}
	if err := validateMinIO(minioSpec(9100)); err != nil {
		t.Error(err)
	}
}

func TestMinIOPublicReadPolicyIsMcDownload(t *testing.T) {
	var doc struct {
		Statement []struct {
			Effect    string
			Principal map[string][]string
			Action    []string
			Resource  []string
		}
	}
	if err := json.Unmarshal([]byte(minioPublicReadPolicy("avatars")), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Statement) != 2 ||
		!slices.Equal(doc.Statement[0].Resource, []string{"arn:aws:s3:::avatars"}) ||
		!slices.Equal(doc.Statement[1].Action, []string{"s3:GetObject"}) ||
		!slices.Equal(doc.Statement[1].Resource, []string{"arn:aws:s3:::avatars/*"}) ||
		!slices.Equal(doc.Statement[1].Principal["AWS"], []string{"*"}) {
		t.Fatalf("policy %+v", doc)
	}
	for _, s := range doc.Statement {
		for _, a := range s.Action {
			if strings.Contains(a, "Put") || strings.Contains(a, "Delete") {
				t.Fatalf("a public_read bucket grants %s to anyone", a)
			}
		}
	}
}

func TestMinIODriftNamesWhatTheRunningServerCannotTake(t *testing.T) {
	dir := t.TempDir()
	spec := minioSpec(9100)
	if msg := minioDrift(spec, dir); msg != "" {
		t.Errorf("no launch record, but drift %q", msg)
	}
	if err := writeMinIOLaunch(spec, dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(minioLaunchFile(dir))
	if bytes.Contains(b, []byte(spec.MinIORootPassword)) || bytes.Contains(b, []byte(spec.MinIORootUser)) {
		t.Fatalf("the launch record holds a credential: %s", b)
	}
	if msg := minioDrift(spec, dir); msg != "" {
		t.Errorf("same declaration, but drift %q", msg)
	}
	for _, tc := range []struct {
		name   string
		change func(*Spec)
		want   string
	}{
		{"CORS origins", func(s *Spec) { s.MinIOCORSAllowOrigin = "http://localhost:3000" }, `CORS origins "http://localhost:3000" (running: "*")`},
		{"region", func(s *Spec) { s.MinIORegion = "eu-west-1" }, `region "eu-west-1" (running: "us-east-1")`},
		{"console port", func(s *Spec) { s.MinIOConsolePort = 9101 }, "console port 9101 (running: 0)"},
	} {
		changed := spec
		tc.change(&changed)
		if msg := minioDrift(changed, dir); !strings.Contains(msg, tc.want) || !strings.Contains(msg, "forge env down") {
			t.Errorf("a changed %s was not reported with its fix: %q", tc.name, msg)
		}
	}
	all := spec
	all.MinIORegion, all.MinIOConsolePort = "eu-west-1", 9101
	if msg := minioDrift(all, dir); !strings.Contains(msg, "region") || !strings.Contains(msg, "console port") {
		t.Errorf("two changes, but not both named: %q", msg)
	}
}

// The real thing: fetch the pinned MinIO, start it with two buckets, one
// public, and prove what an app and a browser see: the buckets exist, the
// public one serves an object anonymously and the private one refuses, the
// declared CORS origin is allowed. Then restart idempotently with the public
// bucket declared private again (adopted, converged: it closes), and stop.
func TestMinIOStartsConvergesBucketsAndStops(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads (~110 MB, cached) and runs MinIO; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("exercised on unix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	project := t.TempDir()
	spec := minioSpec(freeTCPPort(t))
	spec.MinIORootUser, spec.MinIORootPassword = productionCredentials(t)
	spec.MinIOConsolePort = freeTCPPort(t)
	spec.MinIOCORSAllowOrigin = "http://localhost:3000"
	spec.MinIOBuckets = []MinIOBucket{{Name: "uploads"}, {Name: "avatars", PublicRead: true}}
	if err := Start(ctx, project, spec); err != nil {
		skipIfDownloadUnavailable(t, err)
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _, _ = StopReport(project, spec) })

	client, err := minioClient(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"uploads", "avatars"} {
		if ok, err := client.BucketExists(ctx, b); err != nil || !ok {
			t.Fatalf("bucket %s: exists=%v err=%v", b, ok, err)
		}
		body := strings.NewReader("hello")
		if _, err := client.PutObject(ctx, b, "a.txt", body, int64(body.Len()), minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
			t.Fatalf("put into %s: %v", b, err)
		}
	}
	anon := func(bucket string) int {
		resp, err := http.Get(MinIOEndpoint(spec) + "/" + bucket + "/a.txt")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := anon("avatars"); got != http.StatusOK {
		t.Errorf("anonymous GET of the public bucket: HTTP %d, want 200", got)
	}
	if got := anon("uploads"); got != http.StatusForbidden {
		t.Errorf("anonymous GET of the private bucket: HTTP %d, want 403", got)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodOptions, MinIOEndpoint(spec)+"/uploads/a.txt", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", http.MethodPut)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("CORS preflight from the declared origin: Access-Control-Allow-Origin %q", got)
	}

	// Both listeners answer on loopback and on nothing else. The console
	// comes up beside the API, so it is given a moment.
	var consoleUp bool
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && !consoleUp; time.Sleep(250 * time.Millisecond) {
		consoleUp = portListening(spec.MinIOConsolePort)
	}
	if !consoleUp {
		t.Errorf("the declared console port %d is not listening on 127.0.0.1", spec.MinIOConsolePort)
	}
	if ip := nonLoopbackIPv4(); ip != "" {
		for _, p := range []int{spec.Port, spec.MinIOConsolePort} {
			if conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(p)), time.Second); err == nil {
				_ = conn.Close()
				t.Errorf("port %d answers on %s: it must bind loopback only", p, ip)
			}
		}
	} else {
		t.Log("no non-loopback address on this host; the loopback-only half is not exercised")
	}

	st, err := StatusOf(ctx, project, spec)
	if err != nil || !st.Running || !st.Ready || st.Port != spec.Port || st.Version != MinIOVersion {
		t.Fatalf("status: %+v %v", st, err)
	}

	spec.MinIOBuckets = []MinIOBucket{{Name: "uploads"}, {Name: "avatars"}, {Name: "later"}}
	if err := Start(ctx, project, spec); err != nil {
		t.Fatalf("second start was not idempotent: %v", err)
	}
	if got := anon("avatars"); got != http.StatusForbidden {
		t.Errorf("a bucket declared private again still serves anonymously: HTTP %d", got)
	}
	if ok, err := client.BucketExists(ctx, "later"); err != nil || !ok {
		t.Errorf("a bucket declared while the server ran was not created: %v %v", ok, err)
	}

	wrong := spec
	_, wrong.MinIORootPassword = productionCredentials(t)
	if err := Start(ctx, project, wrong); err == nil || !strings.Contains(err.Error(), "forge env down") {
		t.Errorf("a server started with other credentials was adopted silently: %v", err)
	}

	if stopped, err := StopReport(project, spec); err != nil || !stopped {
		t.Fatalf("stop: %v %v", stopped, err)
	}
	if portListening(spec.Port) {
		t.Fatalf("port %s still held after stop", strconv.Itoa(spec.Port))
	}
	if _, err := os.Stat(filepath.Join(spec.dataDir(project), "data", "uploads")); err != nil {
		t.Errorf("the data did not persist past stop: %v", err)
	}
	for _, d := range []string{"config", "certs"} {
		if _, err := os.Stat(filepath.Join(spec.dataDir(project), d)); err != nil {
			t.Errorf("the %s directory is not beside the data (MinIO's default is ~/.minio): %v", d, err)
		}
	}
}
