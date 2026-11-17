package deploytarget

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompose_Deploy_HappyPath confirms the pull + up -d + ps
// sequence fires in order, and the state file lands.
func TestCompose_Deploy_HappyPath(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{
		outputs: map[string]string{
			// compose ps returns one non-header line referencing the
			// target service — the health check accepts this.
			"docker compose -f docker-compose.yml ps": "edge_1   Up   8080/tcp\n",
		},
	}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	group := ServiceGroup{
		Env:        "prod",
		ProviderID: "compose",
		ImageTag:   "v1.2.3",
		Services: []ResolvedService{
			{
				Name: "edge",
				Compose: &ComposeSpec{
					ComposeFile: "docker-compose.yml",
				},
			},
		},
	}
	if err := p.Deploy(context.Background(), group); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	wantPrefixes := []string{
		// Version probe first: --wait needs compose v2.17+, and asking
		// once per group is cheaper than once per service.
		"docker compose version --short",
		// Ownership BEFORE pull/up: refuse to recreate containers another
		// checkout is running (compose_ownership.go).
		"docker compose -f docker-compose.yml ps --all --format",
		"docker compose -f docker-compose.yml pull edge",
		// --wait by default: the deploy blocks on the service's
		// healthcheck rather than returning at container-create time.
		"docker compose -f docker-compose.yml ps --all --quiet",
		"docker compose -f docker-compose.yml up -d --wait edge",
		"docker compose -f docker-compose.yml ps --all --quiet",
		"docker compose -f docker-compose.yml ps --status running edge",
	}
	if len(r.calls) != len(wantPrefixes) {
		t.Fatalf("call count: want %d, got %d (%v)", len(wantPrefixes), len(r.calls), r.calls)
	}
	for i, prefix := range wantPrefixes {
		if !strings.HasPrefix(r.calls[i], prefix) {
			t.Errorf("call[%d]: want prefix %q, got %q", i, prefix, r.calls[i])
		}
	}
	statePath := filepath.Join(dir, ".forge/state/compose-prod-edge.json")
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("state file should exist at %s, got %v", statePath, err)
	}
}

// TestCompose_Deploy_WithEnvFile threads --env-file through to the up
// command when the spec declares one.
func TestCompose_Deploy_WithEnvFile(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{
		outputs: map[string]string{
			"docker compose -f docker-compose.yml ps": "edge_1   Up\n",
		},
	}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	group := ServiceGroup{
		Env:        "prod",
		ProviderID: "compose",
		Services: []ResolvedService{
			{
				Name: "edge",
				Compose: &ComposeSpec{
					ComposeFile: "docker-compose.yml",
					EnvFile:     ".env.prod",
				},
			},
		},
	}
	if err := p.Deploy(context.Background(), group); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	var upCall string
	for _, c := range r.calls {
		if strings.Contains(c, "up -d") {
			upCall = c
			break
		}
	}
	if !strings.Contains(upCall, "--env-file .env.prod") {
		t.Errorf("up call should include --env-file .env.prod, got %q", upCall)
	}
}

// TestCompose_Deploy_SecretsMergedEnvFileWins confirms resolved secrets
// reach the docker process env as the BASE layer, with env_file entries
// overriding on key conflict.
func TestCompose_Deploy_SecretsMergedEnvFileWins(t *testing.T) {
	dir := t.TempDir()
	// env_file declares ONE key that also exists in Secrets (to prove the
	// file wins) plus a file-only key.
	envFile := filepath.Join(dir, ".env.prod")
	if err := os.WriteFile(envFile, []byte("SHARED=from_file\nFILE_ONLY=f\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	r := &fakeRunner{
		outputs: map[string]string{
			"docker compose -f docker-compose.yml ps": "edge_1   Up\n",
		},
	}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	group := ServiceGroup{
		Env:        "prod",
		ProviderID: "compose",
		Services: []ResolvedService{
			{
				Name: "edge",
				Compose: &ComposeSpec{
					ComposeFile: "docker-compose.yml",
					EnvFile:     envFile,
				},
				Secrets: map[string]string{
					"SHARED":      "from_secret", // overridden by env_file
					"SECRET_ONLY": "s",
				},
			},
		},
	}
	if err := p.Deploy(context.Background(), group); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// The pull call carries the merged overlay. Located by name rather
	// than by index: the version probe now runs first, and a positional
	// assertion would re-break every time the call sequence changes.
	var got map[string]string
	for i, c := range r.calls {
		if strings.Contains(c, "pull edge") {
			got = r.envCalls[i]
		}
	}
	if got == nil {
		t.Fatalf("expected a non-nil env overlay on the pull call, got calls=%v envs=%v", r.calls, r.envCalls)
	}
	if got["SHARED"] != "from_file" {
		t.Errorf("env_file should win on conflict: SHARED = %q, want from_file", got["SHARED"])
	}
	if got["SECRET_ONLY"] != "s" {
		t.Errorf("secret-only key missing: SECRET_ONLY = %q, want s", got["SECRET_ONLY"])
	}
	if got["FILE_ONLY"] != "f" {
		t.Errorf("file-only key missing: FILE_ONLY = %q, want f", got["FILE_ONLY"])
	}
}

// TestCompose_ComposeServiceName_Default confirms the compose service
// defaults to the forge service name when KCL leaves it unset.
func TestCompose_ComposeServiceName_Default(t *testing.T) {
	got := composeServiceName(&ComposeSpec{}, "forge-svc")
	if got != "forge-svc" {
		t.Errorf("default service name: want forge-svc, got %s", got)
	}
}

// TestCompose_ComposeServiceName_Override confirms the explicit
// service field wins when set.
func TestCompose_ComposeServiceName_Override(t *testing.T) {
	got := composeServiceName(&ComposeSpec{Service: "compose-svc"}, "forge-svc")
	if got != "compose-svc" {
		t.Errorf("override: want compose-svc, got %s", got)
	}
}

// TestCompose_Deploy_DryRun confirms --dry-run prints the pull and
// up commands but does NOT exec anything and does NOT write the state
// file. Same contract External honors — preview without side effects.
func TestCompose_Deploy_DryRun(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	group := ServiceGroup{
		Env: "prod", ProviderID: "compose", ImageTag: "v1.2.3",
		DryRun: true,
		Services: []ResolvedService{
			{
				Name:    "edge",
				Compose: &ComposeSpec{ComposeFile: "docker-compose.yml"},
			},
		},
	}
	out := captureStdout(t, func() {
		if err := p.Deploy(context.Background(), group); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
	})
	if len(r.calls) != 0 {
		t.Fatalf("dry-run should NOT exec, got %d call(s): %v", len(r.calls), r.calls)
	}
	wantLines := []string{
		"[DRY-RUN] would run: docker compose -f docker-compose.yml pull edge",
		"[DRY-RUN] would run: docker compose -f docker-compose.yml up -d --wait edge",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("stdout should contain %q, got:\n%s", want, out)
		}
	}
	statePath := filepath.Join(dir, ".forge/state/compose-prod-edge.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("state file should NOT exist after dry-run, got err=%v", err)
	}
}

// TestCompose_Deploy_EnvFileMerges confirms the dotenv contents are
// threaded onto the docker-compose process env so `${VAR}` references
// in the compose file resolve even when the user forgets to export.
func TestCompose_Deploy_EnvFileMerges(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env.prod")
	if err := os.WriteFile(envFile, []byte("POSTGRES_PASSWORD=hunter2\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	r := &fakeRunner{
		outputs: map[string]string{
			"docker compose -f docker-compose.yml ps": "edge_1   Up\n",
		},
	}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	group := ServiceGroup{
		Env: "prod", ProviderID: "compose",
		Services: []ResolvedService{
			{
				Name: "edge",
				Compose: &ComposeSpec{
					ComposeFile: "docker-compose.yml",
					EnvFile:     envFile,
				},
			},
		},
	}
	if err := p.Deploy(context.Background(), group); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// The pull and up calls should both carry the env overlay; the
	// ps call (Output, not Run) does not.
	var pullEnv, upEnv map[string]string
	for i, c := range r.calls {
		if strings.Contains(c, "pull edge") {
			pullEnv = r.envCalls[i]
		}
		if strings.Contains(c, "up -d") && strings.HasSuffix(c, "edge") {
			upEnv = r.envCalls[i]
		}
	}
	if pullEnv == nil || pullEnv["POSTGRES_PASSWORD"] != "hunter2" {
		t.Errorf("pull env should include POSTGRES_PASSWORD=hunter2, got %v", pullEnv)
	}
	if upEnv == nil || upEnv["POSTGRES_PASSWORD"] != "hunter2" {
		t.Errorf("up env should include POSTGRES_PASSWORD=hunter2, got %v", upEnv)
	}
}

// TestComposeHasRunningLine_Header confirms the header line is
// skipped when scanning compose ps output.
func TestComposeHasRunningLine_Header(t *testing.T) {
	out := []byte("NAME      STATUS\nedge_1    Up\n")
	if !composeHasRunningLine(out, "edge") {
		t.Error("should find edge in body, ignoring header")
	}
	if composeHasRunningLine(out, "missing") {
		t.Error("should NOT find a service that isn't in body")
	}
}

// TestCompose_Deploy_WaitTimeoutCeiling confirms a declared timeout
// reaches the up command as a CEILING alongside --wait — readiness is
// still the mechanism, the timeout only bounds a wedged container.
func TestCompose_Deploy_WaitTimeoutCeiling(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{outputs: map[string]string{
		"docker compose version --short":          "2.34.0\n",
		"docker compose -f docker-compose.yml ps": "edge_1   Up\n",
	}}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	err := p.Deploy(context.Background(), ServiceGroup{
		Env:        "prod",
		ProviderID: "compose",
		Services: []ResolvedService{{
			Name: "edge",
			Compose: &ComposeSpec{
				ComposeFile:        "docker-compose.yml",
				WaitTimeoutSeconds: 90,
			},
		}},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	var upCall string
	for _, c := range r.calls {
		if strings.Contains(c, "up -d") {
			upCall = c
		}
	}
	if !strings.Contains(upCall, "--wait --wait-timeout 90") {
		t.Errorf("up call should carry --wait --wait-timeout 90, got %q", upCall)
	}
}

// TestCompose_Deploy_WaitOptOut confirms `wait = False` in KCL removes
// the readiness flags entirely — the 20% who want fire-and-forget are
// not disempowered, and they do not pay the version gate either.
func TestCompose_Deploy_WaitOptOut(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{outputs: map[string]string{
		"docker compose -f docker-compose.yml ps": "edge_1   Up\n",
	}}
	no := false
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	err := p.Deploy(context.Background(), ServiceGroup{
		Env:        "prod",
		ProviderID: "compose",
		Services: []ResolvedService{{
			Name:    "edge",
			Compose: &ComposeSpec{ComposeFile: "docker-compose.yml", Wait: &no},
		}},
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "--wait") {
			t.Errorf("wait=false should not pass --wait, got %q", c)
		}
		if strings.Contains(c, "compose version") {
			t.Errorf("wait=false should not probe the compose version, got %q", c)
		}
	}
}

// TestCompose_Deploy_OldComposeFailsLoudly pins the rule that forge
// REFUSES rather than silently not waiting: a deploy that reports
// success without the ordering guarantee is the failure mode the wait
// exists to prevent.
func TestCompose_Deploy_OldComposeFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{outputs: map[string]string{
		"docker compose version --short": "2.5.0\n",
	}}
	p := ComposeProvider{ProjectDir: dir, Runner: r}
	err := p.Deploy(context.Background(), ServiceGroup{
		Env:        "prod",
		ProviderID: "compose",
		Services: []ResolvedService{{
			Name:    "edge",
			Compose: &ComposeSpec{ComposeFile: "docker-compose.yml"},
		}},
	})
	if err == nil {
		t.Fatal("expected an error on a compose too old for --wait")
	}
	if !strings.Contains(err.Error(), "v2.17.0") || !strings.Contains(err.Error(), "wait = False") {
		t.Errorf("error should name the required version and the opt-out, got %v", err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "up -d") {
			t.Error("should not have attempted the up after failing the version gate")
		}
	}
}

// TestComposeVersionSupportsWait covers the version-string shapes the
// docker CLI actually emits, including Docker Desktop's build suffix
// (which semver would otherwise read as a PRE-release, sorting it
// before the release and rejecting a version that is fine).
func TestComposeVersionSupportsWait(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"2.34.0-desktop.1", true},
		{"v2.17.0", true},
		{"2.17.0", true},
		{"2.16.0", false},
		{"v2.5.0", false},
		{"Docker Compose version v2.20.2", true},
		{"  2.18.1\n", true},
		// Unparseable: assume support rather than refuse to deploy over
		// a version string forge has never seen.
		{"garbage", true},
		{"", true},
	} {
		if got := composeVersionSupportsWait(tc.raw); got != tc.want {
			t.Errorf("composeVersionSupportsWait(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
