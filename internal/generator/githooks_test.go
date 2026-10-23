package generator

import (
	"os"
	"path/filepath"
	"testing"
)

// Every hook forge writes must be recognisable as forge's: the CLI activates
// .githooks (core.hooksPath) only when IsForgeManagedGitHook accepts the
// pre-commit there. A header edit that broke the match would silently stop
// activating forge's own hooks in every fresh clone.
func TestGenerateGitHooks_AllRecognisedAsForgeManaged(t *testing.T) {
	g := &ProjectGenerator{Name: "demo", Path: t.TempDir()}
	if err := g.generateGitHooks(); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"pre-commit", "pre-push", "commit-msg"} {
		script, err := os.ReadFile(filepath.Join(g.Path, ".githooks", stage))
		if err != nil {
			t.Fatal(err)
		}
		if !IsForgeManagedGitHook(script) {
			t.Errorf(".githooks/%s is not recognised as forge-managed; its first lines:\n%.200s", stage, script)
		}
	}
}

// A project's own hook — what `project new --in-place` keeps — is never
// forge's, even when it mentions forge.
func TestIsForgeManagedGitHook_ProjectHooks(t *testing.T) {
	for name, script := range map[string]string{
		"empty":                  "",
		"no shebang":             "# forge-managed git pre-commit hook\nexit 0\n",
		"project hook":           "#!/bin/bash\n# Pre-commit hook to format staged Go files\nmake fmt\n",
		"mentions forge later":   "#!/bin/sh\n# our hook\n# forge-managed git pre-commit hook\nforge lint\n",
		"shebang only":           "#!/bin/sh",
		"header without shebang": "\n# forge-managed git pre-commit hook\n",
	} {
		if IsForgeManagedGitHook([]byte(script)) {
			t.Errorf("%s: IsForgeManagedGitHook(%q) = true, want false", name, script)
		}
	}
}
