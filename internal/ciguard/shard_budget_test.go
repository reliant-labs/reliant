package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShardReportsCommandWallBudget(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-test-shard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-go-test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"shard.sh":                    string(body),
		"scripts/ci-go-test.sh":       string(helper),
		"scripts/ci-test-packages.sh": "#!/usr/bin/env bash\necho fixture/package\n",
		"go": `#!/usr/bin/env bash
printf '%s\n' '{"Action":"pass","Package":"fixture/package","Test":"TestCheap","Elapsed":0.01}' '{"Action":"pass","Package":"fixture/package","Elapsed":0.01}'
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "shard.sh", "0", "1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "CI_TEST_JSON="+filepath.Join(root, "events.json"), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"), "CI_TEST_BUDGET_SECONDS=-1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shard: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "::warning::test shard exceeded") {
		t.Fatalf("over-budget command must be visible, not silently green:\n%s", out)
	}
	summary, err := os.ReadFile(filepath.Join(root, "summary"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "Command wall time:") {
		t.Fatalf("summary must include command wall, not just test body elapsed: %s", summary)
	}
}
