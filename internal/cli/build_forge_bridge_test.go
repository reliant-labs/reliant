package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// fakeBridgeGit answers the check's git questions from literals.
type fakeBridgeGit struct {
	headSHA   string
	refs      map[string]string // rev -> commit
	ancestors map[string]bool   // "a..b" -> a is b or an ancestor of b
	remote    bool              // the checkout has a remote
	onRemote  map[string]bool
}

func (f fakeBridgeGit) head(string) string                 { return f.headSHA }
func (f fakeBridgeGit) resolve(_, rev string) string       { return f.refs[rev] }
func (f fakeBridgeGit) isAncestor(_, a, b string) bool     { return a == b || f.ancestors[a+".."+b] }
func (f fakeBridgeGit) published(_, s string) (bool, bool) { return f.onRemote[s], f.remote }

const (
	bridgeHead = "1111111111111111111111111111111111111111"
	bridgeTag  = "2222222222222222222222222222222222222222"
)

func useFakeBridgeGit(t *testing.T, g forgeBridgeGit) {
	t.Helper()
	prev := bridgeGit
	bridgeGit = g
	t.Cleanup(func() { bridgeGit = prev })
}

// bridgedProject writes a project requiring forge at pin and a forge checkout
// at checkout, bridged by the project's go.work. extraGoMod is appended to the
// project's go.mod.
func bridgedProject(t *testing.T, pin, checkout, extraGoMod string) string {
	t.Helper()
	project := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "go.mod"), "module example.com/notes\n\ngo 1.26\n\nrequire github.com/reliant-labs/forge "+pin+"\n"+extraGoMod)
	if checkout != "" {
		if !filepath.IsAbs(checkout) {
			checkout = filepath.Join(project, checkout)
		}
		write(filepath.Join(checkout, "go.mod"), "module github.com/reliant-labs/forge\n\ngo 1.26\n")
		write(filepath.Join(project, "go.work"), "go 1.26\n\nuse (\n\t.\n\t"+checkout+"\n)\n")
	} else {
		write(filepath.Join(project, "go.work"), "go 1.26\n\nuse .\n")
	}
	t.Setenv("GOWORK", "")
	t.Setenv(allowLocalForgeBridgeEnv, "")
	return project
}

// The reported case: go.work bridges forge to a checkout with commits past the
// release go.mod pins. Refused before any build, naming the go.mod line, the
// bridge, and the `go get` of the checkout's commit.
func TestForgeBridge_OlderPinIsRefusedWithTheGoGetLine(t *testing.T) {
	useFakeBridgeGit(t, fakeBridgeGit{
		headSHA:   bridgeHead,
		refs:      map[string]string{"refs/tags/v1.10.0": bridgeTag},
		ancestors: map[string]bool{bridgeTag + ".." + bridgeHead: true},
		remote:    true, onRemote: map[string]bool{bridgeHead: true},
	})
	project := bridgedProject(t, "v1.10.0", t.TempDir(), "")
	err := refuseUnbuildableForgeBridge(project, true)
	if err == nil {
		t.Fatal("a bridge past the pin was not refused")
	}
	for _, want := range []string{
		"go.mod:5 requires github.com/reliant-labs/forge v1.10.0",
		filepath.Join(project, "go.work") + ":5",
		"go get github.com/reliant-labs/forge@" + bridgeHead,
		"replace github.com/reliant-labs/forge => ",
		allowLocalForgeBridgeEnv + "=1",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "push it first") {
		t.Errorf("a published commit was told to be pushed:\n%v", err)
	}
}

// Every project without a forge bridge builds as it always did.
func TestForgeBridge_NoBridgePasses(t *testing.T) {
	useFakeBridgeGit(t, fakeBridgeGit{headSHA: bridgeHead})
	project := bridgedProject(t, "v1.10.0", "", "")
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("a go.work that bridges no forge was refused: %v", err)
	}
	t.Setenv("GOWORK", "off")
	project = bridgedProject(t, "v1.10.0", t.TempDir(), "")
	t.Setenv("GOWORK", "off")
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("GOWORK=off was refused: %v", err)
	}
}

// $GOWORK selects the workspace the go command uses, as an absolute path: a
// bridge it names is judged wherever the project is, and a relative one —
// which the go command refuses outright ("invalid GOWORK: not an absolute
// path") — selects no workspace, so nothing compiles through it.
func TestForgeBridge_ExplicitGOWORK(t *testing.T) {
	useFakeBridgeGit(t, fakeBridgeGit{
		headSHA: bridgeHead, refs: map[string]string{"refs/tags/v1.10.0": bridgeTag},
		ancestors: map[string]bool{bridgeTag + ".." + bridgeHead: true},
	})
	project := bridgedProject(t, "v1.10.0", t.TempDir(), "")
	work := filepath.Join(t.TempDir(), "elsewhere.work")
	if err := os.Rename(filepath.Join(project, "go.work"), work); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", work)
	err := refuseUnbuildableForgeBridge(project, true)
	if err == nil || !strings.Contains(err.Error(), work+":5") {
		t.Errorf("an absolute GOWORK's bridge was not judged: %v", err)
	}
	t.Chdir(filepath.Dir(work))
	t.Setenv("GOWORK", filepath.Base(work))
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("a relative GOWORK, which the go command refuses, was judged: %v", err)
	}
}

// The hatch, and an image that compiles nothing itself, are not judged.
func TestForgeBridge_HatchAndHostBuiltImagesPass(t *testing.T) {
	useFakeBridgeGit(t, fakeBridgeGit{
		headSHA: bridgeHead, refs: map[string]string{"refs/tags/v1.10.0": bridgeTag},
		ancestors: map[string]bool{bridgeTag + ".." + bridgeHead: true},
	})
	project := bridgedProject(t, "v1.10.0", t.TempDir(), "")
	if err := refuseUnbuildableForgeBridge(project, false); err != nil {
		t.Errorf("an image that copies host-built binaries was refused: %v", err)
	}
	t.Setenv(allowLocalForgeBridgeEnv, "1")
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("%s=1 did not skip the check: %v", allowLocalForgeBridgeEnv, err)
	}
}

// A checkout inside the build context, or a pin at or past the checkout's
// HEAD, is what the image compiles: nothing to refuse.
func TestForgeBridge_CheckoutInsideTheContextOrAtThePinPasses(t *testing.T) {
	useFakeBridgeGit(t, fakeBridgeGit{
		headSHA: bridgeHead, refs: map[string]string{"refs/tags/v1.10.0": bridgeTag, "refs/tags/v1.11.0": bridgeHead},
		ancestors: map[string]bool{bridgeTag + ".." + bridgeHead: true},
	})
	project := bridgedProject(t, "v1.10.0", "third_party/forge", "\nreplace github.com/reliant-labs/forge => ./third_party/forge\n")
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("a checkout inside the build context was refused: %v", err)
	}
	project = bridgedProject(t, "v1.11.0", t.TempDir(), "")
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("a pin at the checkout's HEAD was refused: %v", err)
	}
	// A pin the checkout cannot resolve needs the network to judge.
	project = bridgedProject(t, "v1.12.0", t.TempDir(), "")
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("an unresolvable pin was refused offline: %v", err)
	}
}

// A replace to a path outside the context, and a pseudo-version of a commit
// no remote carries, are versions the image's download cannot fetch.
func TestForgeBridge_UnfetchablePinsAreRefused(t *testing.T) {
	useFakeBridgeGit(t, fakeBridgeGit{
		headSHA: bridgeHead, refs: map[string]string{"111111111111": bridgeHead},
		remote: true,
	})
	outside := t.TempDir()
	project := bridgedProject(t, "v1.10.0", outside, "\nreplace github.com/reliant-labs/forge => "+outside+"\n")
	if err := refuseUnbuildableForgeBridge(project, true); err == nil || !strings.Contains(err.Error(), "outside the build context") {
		t.Errorf("a replace outside the context: got %v", err)
	}
	project = bridgedProject(t, "v1.10.1-0.20261125000000-111111111111", t.TempDir(), "")
	err := refuseUnbuildableForgeBridge(project, true)
	if err == nil || !strings.Contains(err.Error(), "no remote branch carries") || !strings.Contains(err.Error(), "push it first") {
		t.Errorf("an unpublished pseudo-version: got %v", err)
	}
}

// The real git probe agrees with the fake on a real repository: a tag behind
// HEAD is refused, HEAD's own tag passes.
func TestForgeBridge_RealGit(t *testing.T) {
	testkit.Slow(t, "execs git")
	checkout := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", checkout, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "release")
	git("tag", "v1.10.0")
	git("commit", "-q", "--allow-empty", "-m", "HandoffStartPath")
	git("tag", "v1.11.0")

	project := bridgedProject(t, "v1.10.0", checkout, "")
	if err := refuseUnbuildableForgeBridge(project, true); err == nil || !strings.Contains(err.Error(), "go get github.com/reliant-labs/forge@") {
		t.Errorf("a tag behind HEAD: got %v", err)
	}
	project = bridgedProject(t, "v1.11.0", checkout, "")
	if err := refuseUnbuildableForgeBridge(project, true); err != nil {
		t.Errorf("HEAD's own tag was refused: %v", err)
	}
}

// The scaffolded Dockerfile compiles in the image; one that copies host-built
// binaries does not.
func TestProjectImageCompilesGo(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "Dockerfile")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if !projectImageCompilesGo(write("FROM golang AS builder\nENV GOWORK=off\nRUN go mod download\nRUN CGO_ENABLED=0 go build -o /app/bin/api ./cmd/api\n")) {
		t.Error("the scaffold's in-image build was not recognised")
	}
	if projectImageCompilesGo(write("FROM gcr.io/distroless/static\nCOPY bin/api /api\n")) {
		t.Error("an image of host-built binaries was judged to compile Go")
	}
}
