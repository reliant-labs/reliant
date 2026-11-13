package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestPinForgeBuildContract(t *testing.T) {
	testkit.Slow(t, "executes pin script with hermetic git/go/task stubs")
	script, err := os.ReadFile("../../../scripts/pin-forge.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, packages string
		fail           bool
		want           []string
	}{
		{name: "default", want: []string{"build", "./..."}},
		{name: "scope", packages: "./cmd/reliant ./internal/auth", want: []string{"build", "./cmd/reliant", "./internal/auth"}},
		{name: "failure", packages: "./cmd/reliant", fail: true, want: []string{"build", "./cmd/reliant"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			write := func(name, body string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			write("pin.sh", string(script), 0755)
			write("go.mod", "module fixture\nrequire (\n github.com/reliant-labs/forge v1.3.1-0.20261109191431-de8e6d23783c\n)\n", 0644)
			write("forge.yaml", "forge_version: old\n", 0644)
			write("bin/git", "#!/bin/sh\ncase \"$1\" in rev-parse) printf '%s\\n' \"$FIXTURE\";; ls-remote) :;; status) :;; *) exit 19;; esac\n", 0755)
			write("bin/go", `#!/usr/bin/env python3
import os,sys,json
args=sys.argv[1:]
if args[:2]==['env','GOFLAGS']: print('-trimpath');sys.exit(0)
with open(os.environ['CALLS'],'a') as f: f.write(json.dumps({'args':args,'gowork':os.environ.get('GOWORK','')})+'\n')
if args and args[0]=='build' and os.environ.get('FAIL_BUILD')=='1': sys.exit(37)
`, 0755)
			write("bin/task", "#!/bin/sh\nprintf 'task-generation\\n' >> \"$CALLS\"\n", 0755)
			cmd := exec.Command("bash", filepath.Join(dir, "pin.sh"), "de8e6d23783c84ea1195b0f90ad2400e930363b4")
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "PIN_BUILD_PACKAGES=") && !strings.HasPrefix(e, "GOFLAGS=") {
					cmd.Env = append(cmd.Env, e)
				}
			}
			calls := filepath.Join(dir, "calls")
			cmd.Env = append(cmd.Env, "PATH="+bin+":"+os.Getenv("PATH"), "FIXTURE="+dir, "CALLS="+calls, "PIN_BUILD_PACKAGES="+tc.packages, "FAIL_BUILD="+map[bool]string{true: "1", false: "0"}[tc.fail])
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("exit=%v output=%s", err, out)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			builds := 0
			generated := false
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if line == "task-generation" {
					generated = true
					continue
				}
				var row struct {
					Args   []string `json:"args"`
					GoWork string   `json:"gowork"`
				}
				if err := json.Unmarshal([]byte(line), &row); err != nil {
					t.Fatal(err)
				}
				if len(row.Args) > 0 && row.Args[0] == "build" {
					builds++
					if !reflect.DeepEqual(row.Args, tc.want) || row.GoWork != "off" {
						t.Fatalf("build=%+v want %v GOWORK=off", row, tc.want)
					}
				}
				if len(row.Args) > 0 && row.Args[0] == "run" {
					generated = true
				}
			}
			if builds != 1 {
				t.Fatalf("build calls=%d", builds)
			}
			if generated == tc.fail {
				t.Fatalf("generation=%v failed-build=%v calls=%s", generated, tc.fail, data)
			}
		})
	}
}
