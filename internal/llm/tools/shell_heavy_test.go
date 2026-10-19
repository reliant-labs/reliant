// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// A command the model declares heavy reaches the daemon declared, on both the
// foreground and the background path, and the schema tells the model when to.
func TestShellTool_HeavyReachesTheDaemon(t *testing.T) {
	for _, bg := range []bool{false, true} {
		d, resp := runShellParams(t, ShellParams{Command: "./scripts/build-all.sh", Heavy: true, RunInBackground: bg})
		if resp.IsError || len(d.reqs) != 1 || !d.reqs[0].Heavy {
			t.Fatalf("background=%v: resp=%+v reqs=%+v", bg, resp, d.reqs)
		}
	}
	d, _ := runShellParams(t, ShellParams{Command: "ls"})
	if d.reqs[0].Heavy {
		t.Fatal("an undeclared command reached the daemon declared heavy")
	}

	schema, err := json.Marshal(NewShellTool(ShellPlatformUnix).ParamSchema())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), "heavy slots") {
		t.Fatalf("the heavy parameter carries no description: %s", schema)
	}
}
