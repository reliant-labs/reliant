// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/auth"
)

// ONE LOGIN on the path an Electron user actually takes.
//
// This is the regression these tests exist for. The deposit was wired to
// `reliant auth login` and to interactive `daemon register` — and an Electron
// user runs NEITHER. Their PAT is minted by electron/src/daemon-creds.js in
// JavaScript, which writes ~/.reliant/daemon.json directly; the daemon then
// takes the "credentials already on disk" branch, which deposited nothing. The
// owner's laptop was signed in to prod and ~/.config/forge/credentials.json
// held only the local dev origin.

// isolateDaemonHome points HOME (and so both ~/.reliant/daemon.json and
// forge's ~/.config/forge/credentials.json) at a temp dir, so no test can read
// or write a developer's real credentials. Returns forge's credentials path.
func isolateDaemonHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv("FORGE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".reliant"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, credentials.FileName)
	return path
}

// fakeAPIServer stands in for reliant-api-server: it publishes RFC 8414
// metadata naming the control plane as the issuer, exactly as prod does
// (verified live: api.reliantapi.com advertises issuer
// https://admin.reliantapi.com).
func fakeAPIServer(t *testing.T, controlPlane string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 controlPlane,
			"authorization_endpoint": controlPlane + "/oauth/authorize",
			"token_endpoint":         controlPlane + "/oauth/token",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDaemonStart_ElectronMintedPATLogsForgeIn is the owner's exact scenario.
// Electron minted the PAT and wrote daemon.json itself, so registration never
// ran. Resolving credentials for the daemon must still leave forge logged in
// to the control plane.
func TestDaemonStart_ElectronMintedPATLogsForgeIn(t *testing.T) {
	credPath := isolateDaemonHome(t)
	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIServer(t, controlPlane)
	const pat = "rlat_ELECTRONMINTED0000"

	// Exactly what electron/src/daemon-creds.js leaves behind: a PAT on disk
	// for the --server origin, with no issuer recorded anywhere.
	if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT:       pat,
		ServerURL: api.URL,
	}); err != nil {
		t.Fatalf("seeding Electron's daemon.json: %v", err)
	}

	conn := &connection{ServerURL: api.URL}
	cmd := &cobra.Command{}
	cmd.SetOut(os.NewFile(0, os.DevNull))

	creds, err := resolveOrAwaitCredentials(context.Background(), cmd, conn, "", t.TempDir(), true)
	if err != nil {
		t.Fatalf("resolveOrAwaitCredentials: %v", err)
	}
	if creds.PAT != pat {
		t.Fatalf("daemon resolved PAT %q, want the seeded one", creds.PAT)
	}

	got, err := credentials.Lookup(credPath, controlPlane, cloudcred.HostClientID)
	if err != nil {
		t.Fatalf("forge is still logged out of %s after the daemon started: %v", controlPlane, err)
	}
	if got.Token != pat {
		t.Errorf("forge holds token %q, want the daemon's PAT", got.Token)
	}
}

// TestDaemonStart_DepositIsKeyedByTheControlPlaneNotTheAPIServer: depositing
// under --server would write an entry forge never looks up, and the symptom is
// the confusing one — signed in to Reliant, forge still says log in.
func TestDaemonStart_DepositIsKeyedByTheControlPlaneNotTheAPIServer(t *testing.T) {
	credPath := isolateDaemonHome(t)
	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIServer(t, controlPlane)

	if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT:       "rlat_KEYEDBYISSUER0000",
		ServerURL: api.URL,
	}); err != nil {
		t.Fatal(err)
	}

	conn := &connection{ServerURL: api.URL}
	cmd := &cobra.Command{}
	cmd.SetOut(os.NewFile(0, os.DevNull))
	if _, err := resolveOrAwaitCredentials(context.Background(), cmd, conn, "", t.TempDir(), true); err != nil {
		t.Fatal(err)
	}

	if _, err := credentials.Lookup(credPath, api.URL, cloudcred.HostClientID); err == nil {
		t.Error("the deposit landed under the API server origin; forge never looks there")
	}
}

// TestDaemonStart_SelfHostedServerIsNotAnError: a self-hosted reliant with no
// control plane publishes no metadata. There is nothing to deposit, and the
// daemon must still start — this runs on every boot, so it cannot be noisy or
// fatal for that population.
func TestDaemonStart_SelfHostedServerIsNotAnError(t *testing.T) {
	credPath := isolateDaemonHome(t)
	selfHosted := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer selfHosted.Close()

	if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT:       "rlat_SELFHOSTEDPAT0000",
		ServerURL: selfHosted.URL,
	}); err != nil {
		t.Fatal(err)
	}

	conn := &connection{ServerURL: selfHosted.URL}
	cmd := &cobra.Command{}
	cmd.SetOut(os.NewFile(0, os.DevNull))
	creds, err := resolveOrAwaitCredentials(context.Background(), cmd, conn, "", t.TempDir(), true)
	if err != nil {
		t.Fatalf("a self-hosted daemon failed to start: %v", err)
	}
	if creds.PAT != "rlat_SELFHOSTEDPAT0000" {
		t.Error("self-hosted daemon did not get its credential")
	}
	// Nothing deposited, and in particular no entry under a guessed origin.
	if f, err := credentials.Load(credPath); err == nil && len(f.Endpoints()) != 0 {
		t.Errorf("self-hosted deposit wrote entries for %v; want none", f.Endpoints())
	}
}

// TestDaemonStart_LeavesAHumanForgeLoginAlone: a `forge login` the user ran
// deliberately must survive a daemon start, since forge resolves its own entry
// first and the deposit is only a fallback.
func TestDaemonStart_LeavesAHumanForgeLoginAlone(t *testing.T) {
	credPath := isolateDaemonHome(t)
	const controlPlane = "https://admin.reliantapi.com"
	api := fakeAPIServer(t, controlPlane)

	if err := credentials.Store(credPath, controlPlane, "forge-cli",
		credentials.Credential{Token: "rlat_HUMANFORGELOGIN0"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{
		PAT:       "rlat_DAEMONDEPOSIT0000",
		ServerURL: api.URL,
	}); err != nil {
		t.Fatal(err)
	}

	conn := &connection{ServerURL: api.URL}
	cmd := &cobra.Command{}
	cmd.SetOut(os.NewFile(0, os.DevNull))
	if _, err := resolveOrAwaitCredentials(context.Background(), cmd, conn, "", t.TempDir(), true); err != nil {
		t.Fatal(err)
	}

	human, err := credentials.Lookup(credPath, controlPlane, "forge-cli")
	if err != nil || human.Token != "rlat_HUMANFORGELOGIN0" {
		t.Fatalf("a daemon start disturbed the user's own `forge login`: %v (%q)", err, human.Token)
	}
}
