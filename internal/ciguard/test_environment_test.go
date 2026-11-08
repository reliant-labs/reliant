package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoTestDropsOnlyTheAmbientSlotLease(t *testing.T) {
	body := readText(t, filepath.Join("..", "..", "scripts", "ci-go-test.sh"))
	root := t.TempDir()
	for name, content := range map[string]string{
		"test.sh": body,
		"go": `#!/usr/bin/env bash
if [[ ${FORGE_SLOT_HELD+x} ]]; then echo "lease reached cmd/go"; exit 1; fi
[[ $FIXTURE_INPUT == preserved ]] || exit 2
printf '%s\n' "$*"
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(root, "test.sh"), "-short", "./fixture")
	cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "FORGE_SLOT_HELD=heavy:1:unique-lease", "FIXTURE_INPUT=preserved")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "test -short ./fixture" {
		t.Fatalf("test helper: %v\n%s", err, out)
	}
	for _, script := range []string{"ci-test-shard.sh", "ci-changed-tests.sh"} {
		src := readText(t, filepath.Join("..", "..", "scripts", script))
		if !strings.Contains(src, "scripts/ci-go-test.sh") {
			t.Errorf("%s must use the same test environment as the cache warmer", script)
		}
	}
}
