package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2/registry/remote/errcode"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/dockercred"
	"github.com/reliant-labs/forge/internal/hostedimage"
)

// An anonymous deploy logs docker in to the platform registry inside a
// throwaway config that pins the host to config.json, so the login never
// reaches the user's keychain. The site and source-map pushes (oras) must read
// that same login. They read the user's credsStore instead, and every one of
// them 401'd with "the supplied credential is not valid" right after the API
// image pushed fine (forge eval 2026-11-17, Docker Desktop's credsStore
// "desktop").
func TestIsolatedLoginIsWhatTheOrasPushesPresent(t *testing.T) {
	const host = "registry.example.com"
	user := t.TempDir()
	// The user's own config: a credsStore helper that does not exist here, so
	// a read that reaches it fails instead of touching this machine's keychain.
	writeDockerConfigJSON(t, filepath.Join(user, "config.json"), map[string]any{"credsStore": "forge-test-no-such-helper"})

	dst := t.TempDir()
	// keepCredentials: a deploy that builds a Dockerfile, as the eval's did.
	if err := populateIsolatedDockerConfig(user, dst, true, host); err != nil {
		t.Fatal(err)
	}
	// What `docker login` writes for a host pinned to the file.
	cfg := readDockerConfigJSON(t, filepath.Join(dst, "config.json"))
	cfg["auths"] = map[string]any{host: map[string]string{
		"auth": base64.StdEncoding.EncodeToString([]byte("forge:rlat_anon")),
	}}
	writeDockerConfigJSON(t, filepath.Join(dst, "config.json"), cfg)

	credential, err := dockercred.CredentialFrom(filepath.Join(dst, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := credential(context.Background(), host)
	if err != nil {
		t.Fatalf("the oras pushes read the user's credsStore, not the deploy's login: %v", err)
	}
	if cred.Password != "rlat_anon" {
		t.Fatalf("oras would present %q, want the deploy's login rlat_anon", cred.Password)
	}
}

func refusedPush(repository string) error {
	return &hostedimage.PushError{Repository: repository, Err: &errcode.ErrorResponse{
		Method: "POST", StatusCode: 401,
		Errors: errcode.Errors{{Code: "UNAUTHORIZED", Message: "the supplied credential is not valid"}},
	}}
}

// The hint names the push that was refused. Source maps are pushed before the
// site through the same error path, and the hint used to name the site's
// repository for a refused maps push.
func TestDeniedPushHintNamesTheRepositoryThatFailed(t *testing.T) {
	const (
		site = "registry.example.com/o/p/web/static.v1"
		maps = "registry.example.com/o/p/web-sourcemaps"
	)
	err := fmt.Errorf("build web: file source maps: push source maps to %s:abc: %w", maps, refusedPush(maps))
	hint := deniedPushHint(err, "", site)
	if !strings.Contains(hint, maps) || strings.Contains(hint, site) {
		t.Fatalf("hint does not name the refused repository %s:\n%s", maps, hint)
	}
}

// An anonymous deploy has no login, token or scopes of the author's to fix,
// so the hint must not send them looking for one.
func TestDeniedPushHintForAnAnonymousDeploy(t *testing.T) {
	restore := cloud.UseClaimableCredential(cloud.Endpoint{URL: "https://cp.example.test"}, "rlat_anon", nil)
	t.Cleanup(restore)

	const site = "registry.example.com/o/p/web/static.v1"
	hint := deniedPushHint(refusedPush(site), "", site)
	for _, want := range []string{site, "--claimable", "claimed", "forge report-bug"} {
		if !strings.Contains(hint, want) {
			t.Errorf("anonymous hint missing %q:\n%s", want, hint)
		}
	}
	for _, wrong := range []string{"registry login", "cloud status", "API tokens", "deploy:write"} {
		if strings.Contains(hint, wrong) {
			t.Errorf("anonymous hint offers %q, which does not exist for an anonymous deploy:\n%s", wrong, hint)
		}
	}
}

func TestDeniedPushHintIgnoresOtherFailures(t *testing.T) {
	if got := deniedPushHint(&hostedimage.PushError{Repository: "r", Err: errors.New("connection reset by peer")}, "", "r"); got != "" {
		t.Errorf("a network error produced a refusal hint: %q", got)
	}
}

func writeDockerConfigJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readDockerConfigJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
