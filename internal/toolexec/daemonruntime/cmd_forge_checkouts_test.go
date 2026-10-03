// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// checkoutsJSON is a canned `forge project checkouts --json` document.
func checkoutsJSON(paths ...string) string {
	type checkout struct {
		Label string `json:"label"`
		Kind  string `json:"kind"`
		Path  string `json:"path,omitempty"`
		Head  string `json:"head"`
		Dirty bool   `json:"dirty"`
	}
	doc := struct {
		Project   string     `json:"project"`
		MainRef   string     `json:"main_ref"`
		Checkouts []checkout `json:"checkouts"`
	}{
		Project: "reliant",
		MainRef: "origin/main",
		// The remote entry carries NO path — there is nothing on disk to
		// render until someone checks it out. Included because it is in
		// forge's real document, and an allowlist that tripped over it
		// would be wrong in production rather than in a fixture.
		Checkouts: []checkout{{Label: "origin/main", Kind: "remote", Head: "abc123"}},
	}
	for _, path := range paths {
		doc.Checkouts = append(doc.Checkouts, checkout{
			Label: filepath.Base(path), Kind: "worktree", Path: path, Head: "def456",
		})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestForgeCheckoutsArgs(t *testing.T) {
	plain := forgeCheckoutsRequest{ProjectPath: "/p"}.args()
	if want := []string{"project", "checkouts", "--json"}; !reflect.DeepEqual(plain, want) {
		t.Errorf("checkouts argv = %v, want %v", plain, want)
	}

	// --tree is opt-in: it costs ~0.5s, and the picker does not need it.
	withTree := forgeCheckoutsRequest{ProjectPath: "/p", WithTree: true}.args()
	if !slices.Contains(withTree, "--tree") {
		t.Errorf("with_tree must add --tree; got %v", withTree)
	}
	if slices.Contains(plain, "--tree") {
		t.Errorf("the default must NOT hash trees; got %v", plain)
	}
}

func TestForgeCheckoutsRequiresProjectPath(t *testing.T) {
	if _, err := handleForgeCheckouts(context.Background(),
		mustDeployPayload(t, forgeCheckoutsRequest{})); err == nil {
		t.Error("checkouts accepted a request with no project_path")
	}
}

// =============================================================================
// THE ALLOWLIST. A project_path is honoured only if forge listed it.
// =============================================================================

// An EMPTY requested checkout means the project's main checkout, and resolves
// without consulting git — it is the root the caller already named.
func TestResolveCheckoutPath_EmptyMeansTheProjectItself(t *testing.T) {
	call := stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON())}, nil)

	got, err := resolveCheckoutPath(context.Background(), "/p", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "/p" {
		t.Errorf("resolved %q, want the project path", got)
	}
	if call.Count != 0 {
		t.Errorf("resolving the default ran %d forge processes; it needs none", call.Count)
	}
}

// A listed checkout resolves, and resolves to FORGE's spelling of the path
// rather than the caller's.
func TestResolveCheckoutPath_AcceptsAListedCheckout(t *testing.T) {
	project := forgeProject(t)
	worktree := t.TempDir()
	stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(worktree))}, nil)

	got, err := resolveCheckoutPath(context.Background(), project, worktree)
	if err != nil {
		t.Fatalf("a listed checkout must be accepted: %v", err)
	}
	if got != worktree {
		t.Errorf("resolved %q, want %q", got, worktree)
	}
}

// THE SECURITY CASE. An unlisted path is refused, so a request cannot point
// forge — which builds images, pushes them and reads secrets — at an arbitrary
// directory on the daemon's filesystem.
//
// The traversal spellings matter: a check that compared raw strings would let
// `<listed>/../../etc` through a naive prefix test, and one that compared only
// exact strings would refuse a legitimate path that differed by a trailing
// slash. Both are covered.
func TestResolveCheckoutPath_RefusesAnUnlistedCheckout(t *testing.T) {
	project := forgeProject(t)
	listed := t.TempDir()

	for name, requested := range map[string]string{
		"an unrelated directory":               t.TempDir(),
		"the filesystem root":                  "/",
		"a traversal out of a listed checkout": filepath.Join(listed, "..", "..", "etc"),
		"a sibling by traversal":               filepath.Join(listed, "..", "elsewhere"),
	} {
		t.Run(name, func(t *testing.T) {
			stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(listed))}, nil)

			got, err := resolveCheckoutPath(context.Background(), project, requested)
			if err == nil {
				t.Fatalf("resolved unlisted path %q to %q; a preview may only render a "+
					"checkout this project actually has", requested, got)
			}
			// The refusal must not echo the daemon's filesystem layout
			// back to the caller.
			if strings.Contains(err.Error(), listed) {
				t.Errorf("the refusal names a path that was not asked about: %v", err)
			}
		})
	}
}

// A path that differs only cosmetically — trailing slash, a redundant `.`, a
// `..` that cancels out — is the SAME directory and must be accepted. Refusing
// it would be a false negative that makes the picker randomly unusable.
func TestResolveCheckoutPath_AcceptsCosmeticallyDifferentSpellings(t *testing.T) {
	project := forgeProject(t)
	listed := t.TempDir()

	for name, requested := range map[string]string{
		"trailing slash":    listed + "/",
		"redundant dot":     filepath.Join(listed, "."),
		"cancelling parent": filepath.Join(listed, "sub", ".."),
	} {
		t.Run(name, func(t *testing.T) {
			stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(listed))}, nil)

			if _, err := resolveCheckoutPath(context.Background(), project, requested); err != nil {
				t.Errorf("%q is the same directory as the listed checkout and must be "+
					"accepted: %v", requested, err)
			}
		})
	}
}

// A symlink to a listed checkout is the same directory, so it resolves.
func TestResolveCheckoutPath_ResolvesSymlinks(t *testing.T) {
	project := forgeProject(t)
	listed := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(listed, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(listed))}, nil)

	if _, err := resolveCheckoutPath(context.Background(), project, link); err != nil {
		t.Errorf("a symlink to a listed checkout is that checkout: %v", err)
	}
}

// A forge that cannot list checkouts must not fail OPEN. With no allowlist
// there is nothing to authorise against, so a specific checkout is refused.
func TestResolveCheckoutPath_RefusesWhenTheListIsUnavailable(t *testing.T) {
	project := forgeProject(t)

	// An old forge: cobra's "unknown command", exit 1, nothing on stdout.
	stubForge(t, forgeCommandResult{
		ExitCode: 1,
		Stderr:   []byte(`Error: unknown command "checkouts" for "forge project"`),
	}, nil)

	if got, err := resolveCheckoutPath(context.Background(), project, t.TempDir()); err == nil {
		t.Errorf("resolved %q with no allowlist available; a checkout must not be authorised "+
			"by a list that could not be read", got)
	}
}

// =============================================================================
// forge.env_diff
// =============================================================================

// forge's `env diff` takes ONE environment or --all, never a list. Pinned
// because the daemon mirrors that rule to fail before a subprocess starts.
func TestForgeEnvDiffArgs(t *testing.T) {
	all := forgeEnvDiffRequest{ProjectPath: "/p", All: true}.args()
	if want := []string{"env", "diff", "--json", "--all"}; !reflect.DeepEqual(all, want) {
		t.Errorf("env diff --all argv = %v, want %v", all, want)
	}

	one := forgeEnvDiffRequest{ProjectPath: "/p", Env: "prod"}.args()
	if want := []string{"env", "diff", "--json", "prod"}; !reflect.DeepEqual(one, want) {
		t.Errorf("env diff argv = %v, want %v", one, want)
	}
}

func TestForgeEnvDiffValidation(t *testing.T) {
	for name, req := range map[string]forgeEnvDiffRequest{
		"no project path":     {Env: "prod"},
		"neither env nor all": {ProjectPath: "/p"},
		"both env and all":    {ProjectPath: "/p", Env: "prod", All: true},
		// An env landing in argv as a bare positional would be read by
		// forge as a flag.
		"flag-shaped env": {ProjectPath: "/p", Env: "--all"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := req.validate(); err == nil {
				t.Errorf("validate accepted %+v", req)
			}
		})
	}
}

// The diff document crosses VERBATIM. The UI renders forge's §8.2 document
// as-is; a diff computed in reliant would be a second implementation that
// disagrees with forge's.
func TestForgeEnvDiffPassesForgeDocumentThrough(t *testing.T) {
	dir := forgeProject(t)
	doc := `{"project":"reliant","source":"worktree","against":"live",` +
		`"environments":[{"env":"prod","status":"ok","config_identical":false}]}`
	stubForge(t, forgeCommandResult{Stdout: []byte(doc)}, nil)

	raw, err := handle(t, "forge.env_diff", map[string]any{"project_path": dir, "all": true})
	if err != nil {
		t.Fatalf("env_diff: %v", err)
	}

	var resp forgeReportResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got, want any
	if err := json.Unmarshal(resp.Report, &got); err != nil {
		t.Fatalf("report is not forge's document: %v", err)
	}
	if err := json.Unmarshal([]byte(doc), &want); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the diff document was altered in transit:\n got %v\nwant %v", got, want)
	}
}

// env_diff authorises its checkout before forge is pointed at it.
func TestForgeEnvDiffRefusesAnUnlistedCheckout(t *testing.T) {
	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{Stdout: []byte(checkoutsJSON(t.TempDir()))}, nil)

	if _, err := handle(t, "forge.env_diff", map[string]any{
		"project_path": dir, "all": true, "checkout_path": t.TempDir(),
	}); err == nil {
		t.Error("env_diff rendered an unlisted checkout")
	}
}

// A handler that is written but not registered is invisible to the RPC layer
// above. Dispatched against a non-forge dir, which short-circuits before forge
// would run, so this asserts dispatch and nothing else.
func TestForgeCheckoutCommandsAreRegistered(t *testing.T) {
	for name, payload := range map[string]map[string]any{
		"forge.checkouts": {"project_path": "PLACEHOLDER"},
		"forge.env_diff":  {"project_path": "PLACEHOLDER", "all": true},
	} {
		t.Run(name, func(t *testing.T) {
			stubForge(t, forgeCommandResult{}, nil)
			payload["project_path"] = t.TempDir()
			if _, err := handle(t, name, payload); err != nil {
				t.Fatalf("%s not dispatchable: %v", name, err)
			}
		})
	}
}
