package templates

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The Taskfile's GOFLAGS script adds -trimpath to the machine's GOFLAGS, and
// keeps any -trimpath setting already there as it is: -trimpath=false is the
// documented opt-out, so it must come through untouched, never with a
// -trimpath appended after it (Go's last value would win).
func TestTaskfileTrimpathGOFLAGSScript_KeepsAnOptOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script is POSIX sh, which Task runs through its own interpreter on Windows")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	var v struct {
		Sh string `yaml:"sh"`
	}
	if err := yaml.Unmarshal([]byte("sh: "+TaskfileTrimpathGOFLAGSScript), &v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(TaskfileTrimpathGOFLAGS, "sh: "+TaskfileTrimpathGOFLAGSScript) {
		t.Fatal("the entry does not run TaskfileTrimpathGOFLAGSScript")
	}
	for in, want := range map[string]string{
		"":                           "-trimpath",
		"-mod=mod":                   "-mod=mod -trimpath",
		"-trimpath":                  "-trimpath",
		"-mod=mod -trimpath":         "-mod=mod -trimpath",
		"-trimpath=false":            "-trimpath=false",
		"-mod=mod -trimpath=false":   "-mod=mod -trimpath=false",
		"--trimpath=false":           "--trimpath=false",
		"-trimpath=true -mod=vendor": "-trimpath=true -mod=vendor",
	} {
		// `go env GOFLAGS` is stubbed: what it prints is the case's input.
		cmd := exec.Command(sh, "-c", `go() { printf '%s\n' "$FAKE_GOFLAGS"; }; `+v.Sh)
		cmd.Env = []string{"FAKE_GOFLAGS=" + in, "PATH=/usr/bin:/bin"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("GOFLAGS %q: %v", in, err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("GOFLAGS %q: the entry yields %q, want %q", in, got, want)
		}
	}
}
