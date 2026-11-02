package a

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"helper"
	"prod"

	"github.com/testcontainers/testcontainers-go"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// ---- flagged ----------------------------------------------------------------

func TestExecsGo(t *testing.T) { // want `TestExecsGo is not marked slow but execs .go. at a_test.go:\d+`
	_ = exec.Command("go", "build", "./...").Run()
}

func TestExecsByConstantPath(t *testing.T) { // want `execs .kcl.`
	const kcl = "/usr/local/bin/kcl"
	_ = exec.CommandContext(context.Background(), kcl, "run").Run()
}

func TestViaHelper(t *testing.T) { // want `calls gitInit, which execs .git.`
	gitInit("testdata/repo")
}

func TestViaSubtestCallback(t *testing.T) { // want `calls npmInstall, which execs .npm.`
	t.Run("install", npmInstall)
}

func TestViaOnceValue(t *testing.T) { // want `calls forgeBinary, which execs .go.`
	_ = forgeBinary()
}

func TestCrossPackage(t *testing.T) { // want `calls helper.Render, which calls BuildForge, which execs .go.`
	_ = helper.Render()
}

func TestStartsContainer(t *testing.T) { // want `drives containers through testcontainers-go.Run`
	_, _ = testcontainers.Run("postgres:16")
}

func TestDialsExternalHost(t *testing.T) { // want `dials the external host in "https://api.github.com/repos"`
	_, _ = http.Get("https://api.github.com/repos")
}

func TestDialsExternalAddress(t *testing.T) { // want `dials the external host "proxy.golang.org:443"`
	var d net.Dialer
	_, _ = d.DialContext(context.Background(), "tcp", "proxy.golang.org:443")
}

func TestInSubtestLiteral(t *testing.T) { // want `execs .forge.`
	t.Run("render", func(t *testing.T) {
		_ = exec.Command("forge", "env", "render", "prod").Run()
	})
}

// ---- marked: not flagged ----------------------------------------------------

func TestMarkedWithTestkit(t *testing.T) {
	testkit.Slow(t, "builds the module", testkit.Inputs("go.mod", "internal/**"))
	_ = exec.Command("go", "build", "./...").Run()
}

func TestMarkedWithShortGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("execs go build")
	}
	_ = exec.Command("go", "build", "./...").Run()
}

func TestBypassesUnderShort(t *testing.T) {
	if !testing.Short() {
		_ = exec.Command("git", "fetch").Run()
	}
}

func TestMarkedInHelper(t *testing.T) {
	gitInitGuarded(t)
}

func TestMarkedByOtherPackageHelper(t *testing.T) {
	_ = helper.GuardedRender(t)
}

func TestMarkedByRawGuardHelper(t *testing.T) {
	helper.RawGuard(t)
	_ = helper.BuildForge()
}

// ---- fast or unknowable: not flagged ----------------------------------------

func TestFast(t *testing.T) {
	_ = helper.Fast()
}

func TestBinaryFromVariable(t *testing.T) {
	bin := t.TempDir() + "/forge"
	_ = exec.Command(bin, "version").Run()
}

func TestCallsProductionCode(t *testing.T) {
	_ = gitHead()
	_ = prod.Deploy()
}

func TestHarmlessBinary(t *testing.T) {
	_ = exec.Command("true").Run()
}

func TestLoopbackAndReservedHosts(t *testing.T) {
	_, _ = http.Get("http://127.0.0.1:8080/healthz")
	_, _ = http.Get("http://localhost/x")
	_, _ = http.Get("https://api.example/x")
	_, _ = net.Dial("tcp", "[::1]:5432")
}

// ---- git: against a temp dir it is not slow work -----------------------------

func TestGitInitInTempDir(t *testing.T) {
	dir := t.TempDir()
	_ = exec.Command("git", "init", dir).Run()
}

func TestGitCmdDirUnderTempDir(t *testing.T) {
	cmd := exec.Command("git", "status")
	cmd.Dir = filepath.Join(t.TempDir(), "repo")
	_ = cmd.Run()
}

func TestGitInMkdirTemp(t *testing.T) {
	dir, _ := os.MkdirTemp("", "repo")
	_ = exec.Command("git", "-C", dir, "log").Run()
}

func TestGitHelperGivenTempDir(t *testing.T) {
	gitIn(t.TempDir(), "init")
}

func TestGitVariadicHelperGivenTempDir(t *testing.T) {
	gitArgs("-C", t.TempDir(), "init")
}

func TestGitAppendHelperGivenTempDir(t *testing.T) {
	gitC(t, t.TempDir(), "init")
}

func TestGitTwoHopsToTempDir(t *testing.T) {
	newRepo(t)
}

func TestGitTempDirInStructField(t *testing.T) {
	base := t.TempDir()
	f := &repoFixture{dir: filepath.Join(base, "repo")}
	gitIn(f.dir, "init")
}

// A field assigned from itself, and two fields assigned from each other,
// must not send the dataflow round in circles.
func TestGitSelfReferentialField(t *testing.T) {
	f := &repoFixture{dir: t.TempDir()}
	f.dir = filepath.Join(f.dir, "sub")
	gitIn(f.dir, "init")
}

func TestGitMutuallyReferentialFields(t *testing.T) { // want `calls gitIn, which execs .git.`
	a, b := &repoFixture{}, &repoFixture{}
	a.dir = b.dir
	b.dir = a.dir
	gitIn(a.dir, "init")
}

func TestGitHelperReturnsTempDir(t *testing.T) {
	gitIn(projectDir(t), "init")
}

func TestGitOtherPackageHelperGivenTempDir(t *testing.T) {
	helper.GitIn(t.TempDir(), "init")
}

// ---- git: over the checkout, or a directory the analyzer cannot follow ------

func TestGitOverCheckout(t *testing.T) { // want `TestGitOverCheckout is not marked slow but execs .git. at a_test.go:\d+`
	_, _ = exec.Command("git", "ls-files").Output()
}

func TestGitHelperGivenCheckout(t *testing.T) { // want `calls gitIn, which execs .git. at a_test.go:\d+`
	gitIn(".", "status")
}

func TestGitCmdDirMixed(t *testing.T) { // want `execs .git.`
	cmd := exec.Command("git", "status")
	cmd.Dir = t.TempDir()
	if len(os.Args) > 5 {
		cmd.Dir = "."
	}
	_ = cmd.Run()
}

func TestGitStructFromElsewhere(t *testing.T) { // want `calls gitIn, which execs .git.`
	f := checkoutFixture()
	gitIn(f.dir, "status")
}

func TestGitHelperReturnsCheckout(t *testing.T) { // want `calls gitIn, which execs .git.`
	gitIn(checkoutDir(), "status")
}

func TestGitOtherPackageHelperGivenCheckout(t *testing.T) { // want `calls helper.GitIn, which execs .git. at helper.go:\d+`
	helper.GitIn(".", "status")
}

// ---- inputs the analyzer cannot read ----------------------------------------

var computedGlob = "deploy/" + suffix()

func suffix() string { return "**" }

var renderInputs = testkit.Inputs("deploy/**")

func TestInputsNotConstant(t *testing.T) {
	testkit.Slow(t, "renders", testkit.Inputs("forge.yaml", computedGlob)) // want `testkit.Inputs argument is not a constant string`
}

func TestInputsThroughVariable(t *testing.T) {
	testkit.Slow(t, "renders", renderInputs) // want `pass testkit.Inputs\(...\) inline`
}

func TestInputsSpread(t *testing.T) {
	globs := []string{"deploy/**"}
	testkit.Slow(t, "renders", testkit.Inputs(globs...)) // want `testkit.Inputs is spread from a slice`
}

// ---- helpers ----------------------------------------------------------------

func gitInit(dir string) {
	_ = exec.Command("git", "init", dir).Run()
}

func gitInitGuarded(t *testing.T) {
	testkit.Slow(t, "inits a git repo")
	gitInit(t.TempDir())
}

func gitIn(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	_ = cmd.Run()
}

func gitArgs(args ...string) {
	_ = exec.Command("git", args...).Run()
}

func gitC(t *testing.T, dir string, args ...string) {
	_ = exec.Command("git", append([]string{"-C", dir}, args...)...).Run()
}

type repoFixture struct{ dir string }

func checkoutFixture() repoFixture { return repoFixture{dir: "."} }

func checkoutDir() string { return "." }

func projectDir(t *testing.T) string {
	return filepath.Join(t.TempDir(), "project")
}

func newRepo(t *testing.T) string {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	gitIn(repo, "init")
	return repo
}

func npmInstall(t *testing.T) {
	_ = exec.Command("npm", "ci").Run()
}

var forgeBinary = sync.OnceValue(func() string {
	out, _ := exec.Command("go", "build", "-o", "forge", "./cmd/forge").Output()
	return string(out)
})
