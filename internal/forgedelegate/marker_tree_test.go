package forgedelegate

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

const treeStageEnv = "FORGEDELEGATE_TEST_TREE_STAGE"

// The marker reaches the forge a hand-off exec'd and nothing it starts: not a
// child that inherits the environment (the marker is gone from it), and not
// one handed a copy (the copy names another pid).
func TestMarker_ProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delegate is exec'd in place, which Windows cannot do")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMarkerTreeHelperProcess$")
	cmd.Env = append(os.Environ(), treeStageEnv+"=exec")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the process tree failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"delegate: delegated=true marker-in-env=false",
		"child(inherited): delegated=false",
		"child(copied): delegated=false",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestMarkerTreeHelperProcess is not a test: re-executed by the test above,
// it plays one process of the tree.
func TestMarkerTreeHelperProcess(t *testing.T) {
	stage := os.Getenv(treeStageEnv)
	switch {
	case stage == "exec":
		var env []string
		for _, kv := range Env("forge_version v1.0.4") {
			if !strings.HasPrefix(kv, treeStageEnv+"=") {
				env = append(env, kv)
			}
		}
		fmt.Println("exec:", Exec(os.Args[0], os.Args[1:], append(env, treeStageEnv+"=delegate")))
		os.Exit(1)
	case stage == "delegate":
		_, inEnv := os.LookupEnv(MarkerEnv)
		fmt.Printf("delegate: delegated=%v marker-in-env=%v\n", Delegated(), inEnv)
		copied := MarkerEntry(os.Getpid(), "forge_version v1.0.4")
		for name, extra := range map[string][]string{"inherited": nil, "copied": {copied}} {
			cmd := exec.Command(os.Args[0], os.Args[1:]...)
			cmd.Env = append(append(os.Environ(), extra...), treeStageEnv+"=child:"+name)
			out, err := cmd.CombinedOutput()
			fmt.Printf("%s", out)
			if err != nil {
				fmt.Printf("child(%s) failed: %v\n", name, err)
			}
		}
		os.Exit(0)
	case strings.HasPrefix(stage, "child:"):
		fmt.Printf("child(%s): delegated=%v\n", strings.TrimPrefix(stage, "child:"), Delegated())
		os.Exit(0)
	}
}
