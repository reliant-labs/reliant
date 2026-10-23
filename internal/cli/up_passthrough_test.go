package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestUpOptionsCarryNoPlacementOverride is the structural guard behind the
// removal of --host-only / --cluster-only.
//
// Those flags did not merely scope a run: --host-only rewrote every
// cluster-declared service into a local `go run` process, so forge overruled
// the environment's own placement decision on the strength of a CLI flag. The
// replacement is declarative — a service that should run on the host says so
// with a `host` block in its KCL, and a render option (`-D host_runner=go-run`)
// selects the runner.
//
// The regression this catches is re-introducing that override under any name.
// upOptions is the only channel from the command line into runUp, so a
// placement-shaped boolean appearing on it is the shape of the mistake, and a
// field list is the one assertion that fails when someone adds one.
func TestUpOptionsCarryNoPlacementOverride(t *testing.T) {
	banned := map[string]string{
		"hostOnly":    "--host-only rewrote cluster-declared services into host processes",
		"clusterOnly": "--cluster-only masked the host/frontend phases off",
		"hostMode":    "placement belongs in the env's KCL, not a flag",
		"forceHost":   "placement belongs in the env's KCL, not a flag",
	}
	typ := reflect.TypeOf(upOptions{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if why, bad := banned[name]; bad {
			t.Errorf("upOptions.%s reintroduces a CLI placement override: %s.\n"+
				"Scope a run with --target; declare placement with a `host` block "+
				"and select the runner with `-D host_runner=...`.", name, why)
		}
	}
}

// TestEnvUpUsesTheWholeLoop pins what the local verb promises. `forge env up`,
// which `env up` absorbed, once implied --host-only — skipping the cluster
// build/deploy entirely and collapsing cluster services to `go run`. The help
// must not promise that skip behaviour, which no longer happens.
func TestEnvUpUsesTheWholeLoop(t *testing.T) {
	cmd := newEnvUpCmd()
	help := cmd.Short + "\n" + cmd.Long
	for _, stale := range []string{"--host-only", "--cluster-only", "skipping cluster build/deploy", "skipping the cluster build + deploy"} {
		if strings.Contains(help, stale) {
			t.Errorf("`forge env up` help still advertises %q, which the command no longer does", stale)
		}
	}
	// The surviving way to narrow a run has to be discoverable from here:
	// this help is where someone lands after the flag they typed went away.
	if !strings.Contains(help, "--target") {
		t.Error("`forge env up` help should point at --target as the way to scope a run to one service")
	}
}

// TestEnvUpHelpDocumentsPassthroughAndLocality covers what V1 moved ONTO this
// command: the `--` passthrough `forge env up` used to own, and the local-only
// contract. Both are discoverable only from this help.
func TestEnvUpHelpDocumentsPassthroughAndLocality(t *testing.T) {
	cmd := newEnvUpCmd()
	help := cmd.Use + "\n" + cmd.Short + "\n" + cmd.Long
	for _, want := range []string{"--", "npm run dev", "forge env deploy"} {
		if !strings.Contains(help, want) {
			t.Errorf("`forge env up` help does not mention %q:\n%s", want, help)
		}
	}
	if !strings.Contains(cmd.Use, "--") {
		t.Errorf("Use should show the passthrough terminator, got %q", cmd.Use)
	}
}

// TestUpPassthroughArgs covers the `--`-terminator split `forge env up` uses
// to separate the env positional from dev-server passthrough.
func TestUpPassthroughArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		dashPos int
		want    []string
		wantErr bool
	}{
		{
			// `forge env up dev` — one positional, no `--`.
			name: "env only no dash", args: []string{"dev"}, dashPos: -1, want: nil,
		},
		{
			// `forge env up dev -- --host 0.0.0.0` — cobra: dash at 1.
			name: "passthrough after dash", args: []string{"dev", "--host", "0.0.0.0"}, dashPos: 1,
			want: []string{"--host", "0.0.0.0"},
		},
		{
			// `forge env up dev --` — terminator present, nothing after it.
			name: "bare dash", args: []string{"dev"}, dashPos: 1, want: nil,
		},
		{
			// `forge env up dev extra` — a second positional with no `--`
			// is a usage error (it used to be swallowed silently).
			name: "two positionals no dash errors", args: []string{"dev", "extra"}, dashPos: -1, wantErr: true,
		},
		{
			// `forge env up -- --host x` — no env before the terminator.
			name: "no env before dash errors", args: []string{"--host", "x"}, dashPos: 0, wantErr: true,
		},
		{
			// `forge env up dev extra -- --host x` — too much before it.
			name: "two positionals before dash errors", args: []string{"dev", "extra", "--host", "x"}, dashPos: 2, wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := upPassthroughArgs(c.args, c.dashPos)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (got=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestBuildFrontendCmd_ForwardsPassthroughArgs pins the load-bearing piece
// of `forge env up dev -- --host 0.0.0.0`: the tokens reach the frontend dev
// server as `npm run dev -- --host 0.0.0.0` (the `--` is required so npm
// forwards them to vite/next rather than consuming them itself).
func TestBuildFrontendCmd_ForwardsPassthroughArgs(t *testing.T) {
	fe := FrontendEntity{Name: "dashboard", Path: "frontends/dashboard", Port: 3000, EnvFile: "/does/not/exist"}
	cmd := buildFrontendCmd(context.Background(), fe, "dev", nil, []string{"--host", "0.0.0.0"}, "")

	want := []string{"run", "dev", "--", "--host", "0.0.0.0"}
	// cmd.Args[0] is the runner (npm); the rest are the run args.
	got := cmd.Args[1:]
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("frontend cmd args: got %v, want [npm %v]", cmd.Args, want)
	}
}

// TestBuildFrontendCmd_NoPassthroughUnchanged confirms the default (no `--`,
// so nil passthrough) leaves the command as the bare `npm run dev` — no
// trailing `--` that some dev servers choke on.
func TestBuildFrontendCmd_NoPassthroughUnchanged(t *testing.T) {
	fe := FrontendEntity{Name: "dashboard", Path: "frontends/dashboard", Port: 3000, EnvFile: "/does/not/exist"}
	cmd := buildFrontendCmd(context.Background(), fe, "dev", nil, nil, "")

	want := []string{"run", "dev"}
	got := cmd.Args[1:]
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("frontend cmd args: got %v, want [npm %v]", cmd.Args, want)
	}
}

// TestNewEnvUpCmd_ParsesPassthrough pins the cobra wiring end to end: the env
// is a positional, `-- <flags>` land in the post-dash passthrough
// (ArgsLenAtDash == 1) rather than being rejected as unknown flags, and the
// split hands back exactly the forwarded tokens.
func TestNewEnvUpCmd_ParsesPassthrough(t *testing.T) {
	cmd := newEnvUpCmd()
	if !strings.HasPrefix(cmd.Use, "up") {
		t.Errorf("Use: got %q, want prefix \"up\"", cmd.Use)
	}
	// The env is a POSITIONAL on this command. `forge env up`'s `--env` flag
	// went away with it; a reintroduced one would make "which env?" have two
	// answers again.
	if cmd.Flags().Lookup("env") != nil {
		t.Error("`forge env up` must not carry an --env flag — the environment is its positional argument")
	}

	if err := cmd.ParseFlags([]string{"dev", "--", "--host", "0.0.0.0"}); err != nil {
		t.Fatalf("parse `dev -- --host 0.0.0.0`: %v", err)
	}
	dash := cmd.ArgsLenAtDash()
	if dash != 1 {
		t.Errorf("ArgsLenAtDash: got %d, want 1", dash)
	}
	got, err := upPassthroughArgs(cmd.Flags().Args(), dash)
	if err != nil {
		t.Fatalf("upPassthroughArgs: %v", err)
	}
	if strings.Join(got, " ") != "--host 0.0.0.0" {
		t.Errorf("passthrough: got %v, want [--host 0.0.0.0]", got)
	}
}
