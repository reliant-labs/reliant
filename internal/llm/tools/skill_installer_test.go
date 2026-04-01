package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

func TestSkillInstaller_DryRunAndInstall(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "hello-skill")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "references"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: hello-skill
description: Hello skill
---
Use it`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "references", "guide.md"), []byte("guide"), 0o644))

	tool := NewSkillInstallerTool().(*ToolWrapper[InstallSkillParams, ToolResponse])
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "c1", Thread: "t1", Project: &db.Project{Path: project}}

	preview, err := tool.tool.Execute(tc, InstallSkillParams{Source: source, Scope: "project", DryRun: true})
	require.NoError(t, err)
	require.False(t, preview.IsError)
	require.Contains(t, preview.Content, "Dry-run preview")

	install, err := tool.tool.Execute(tc, InstallSkillParams{Source: source, Scope: "project", ConflictPolicy: "overwrite"})
	require.NoError(t, err)
	require.False(t, install.IsError)
	require.Contains(t, install.Content, "Installed skill 'hello-skill'")

	_, err = os.Stat(filepath.Join(project, ".reliant", "skills", "hello-skill", "SKILL.md"))
	require.NoError(t, err)
}

func TestSkillInstaller_IncludesAssetsAndAgentsMetadata(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "gh-fix-ci")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "assets"), 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: gh-fix-ci
description: Fix GitHub Actions checks
---
Use this skill.`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "scripts", "inspect_pr_checks.py"), []byte("print('ok')"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "agents", "openai.yaml"), []byte("interface:\n  display_name: GitHub Fix CI\n  icon_small: ./assets/github.png"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "assets", "github.png"), []byte("fake image bytes"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "assets", "README.md"), []byte("asset docs"), 0o644))

	service := NewSkillInstallerService()
	result, err := service.Install(context.Background(), SkillInstallRequest{
		ProjectPath:    project,
		Source:         source,
		Scope:          "project",
		ConflictPolicy: "overwrite",
	})
	require.NoError(t, err)

	require.Equal(t, []string{"SKILL.md", "agents/openai.yaml", "assets/README.md", "assets/github.png", "scripts/inspect_pr_checks.py"}, result.InstalledFiles)
	require.Empty(t, result.SkippedFiles)

	_, err = os.Stat(filepath.Join(project, ".reliant", "skills", "gh-fix-ci", "agents", "openai.yaml"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(project, ".reliant", "skills", "gh-fix-ci", "assets", "github.png"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(project, ".reliant", "skills", "gh-fix-ci", "assets", "README.md"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(project, ".reliant", "skills", "gh-fix-ci", "scripts", "inspect_pr_checks.py"))
	require.NoError(t, err)
}

func TestSkillInstaller_RenamePolicy(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "duplicate-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: duplicate-skill
description: Duplicate skill
---
Use it`), 0o644))

	existingDir := filepath.Join(project, ".reliant", "skills", "duplicate-skill")
	require.NoError(t, os.MkdirAll(existingDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existingDir, "SKILL.md"), []byte(`---
name: duplicate-skill
description: Existing
---
existing`), 0o644))

	tool := NewSkillInstallerTool().(*ToolWrapper[InstallSkillParams, ToolResponse])
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "c1", Thread: "t1", Project: &db.Project{Path: project}}

	resp, err := tool.tool.Execute(tc, InstallSkillParams{Source: source, Scope: "project", ConflictPolicy: "rename"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "rename conflict policy is not supported for SKILL.md skills")
}

func TestSkillInstaller_GitSourceSubpath(t *testing.T) {
	project := t.TempDir()
	repoDir := createSkillRepo(t)

	tool := NewSkillInstallerTool().(*ToolWrapper[InstallSkillParams, ToolResponse])
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "c1", Thread: "t1", Project: &db.Project{Path: project}}

	resp, err := tool.tool.Execute(tc, InstallSkillParams{
		Source:         "file://" + repoDir,
		SourceSubpath:  "skills/network-debug",
		Ref:            "main",
		Scope:          "project",
		ConflictPolicy: "overwrite",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "source_type: git")

	_, err = os.Stat(filepath.Join(project, ".reliant", "skills", "network-debug", "SKILL.md"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(project, ".reliant", "skills", "network-debug", "examples", "playbook.md"))
	require.NoError(t, err)
}

func TestSkillInstaller_GitSourceSubpathTraversalRejected(t *testing.T) {
	project := t.TempDir()
	repoDir := createSkillRepo(t)

	tool := NewSkillInstallerTool().(*ToolWrapper[InstallSkillParams, ToolResponse])
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "c1", Thread: "t1", Project: &db.Project{Path: project}}

	resp, err := tool.tool.Execute(tc, InstallSkillParams{
		Source:        "file://" + repoDir,
		SourceSubpath: "../skills/network-debug",
		Scope:         "project",
	})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "invalid source_subpath")
}

func TestSkillInstaller_GitSourceConflictSkip(t *testing.T) {
	project := t.TempDir()
	repoDir := createSkillRepo(t)

	target := filepath.Join(project, ".reliant", "skills", "network-debug")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(`---
name: network-debug
description: Existing skill
---
existing`), 0o644))

	tool := NewSkillInstallerTool().(*ToolWrapper[InstallSkillParams, ToolResponse])
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "c1", Thread: "t1", Project: &db.Project{Path: project}}

	resp, err := tool.tool.Execute(tc, InstallSkillParams{
		Source:         "file://" + repoDir,
		SourceSubpath:  "skills/network-debug",
		Scope:          "project",
		ConflictPolicy: "skip",
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "skipped due to conflicts: 1")

	blob, readErr := os.ReadFile(filepath.Join(target, "SKILL.md"))
	require.NoError(t, readErr)
	require.Contains(t, string(blob), "description: Existing skill")
}

func TestSkillInstaller_RejectsDestinationSymlinkTarget(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "symlinked-target-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: symlinked-target-skill
description: Skill
---
Use it`), 0o644))

	target := filepath.Join(project, ".reliant", "skills", "symlinked-target-skill")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "original.md"), []byte("original"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(target, "original.md"), filepath.Join(target, "SKILL.md")))

	service := NewSkillInstallerService()
	_, err := service.Install(context.Background(), SkillInstallRequest{
		ProjectPath:    project,
		Source:         source,
		Scope:          "project",
		ConflictPolicy: "overwrite",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")
}

func TestSkillInstaller_RejectsSymlinkInDestinationPathChain(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "path-chain-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: path-chain-skill
description: Skill
---
Use it`), 0o644))

	reliantDir := filepath.Join(project, ".reliant")
	realSkills := filepath.Join(project, "real-skills")
	require.NoError(t, os.MkdirAll(reliantDir, 0o755))
	require.NoError(t, os.MkdirAll(realSkills, 0o755))
	require.NoError(t, os.Symlink(realSkills, filepath.Join(reliantDir, "skills")))

	service := NewSkillInstallerService()
	_, err := service.Install(context.Background(), SkillInstallRequest{
		ProjectPath: project,
		Source:      source,
		Scope:       "project",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")
}

func TestSkillInstaller_AtomicValidationFailureDoesNotCreateTargetDir(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "atomic-fail-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: atomic-fail-skill
description: Skill
---
Use it`), 0o644))

	targetDir := filepath.Join(project, ".reliant", "skills", "atomic-fail-skill")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte("invalid"), 0o644))

	service := NewSkillInstallerService()
	_, err := service.Install(context.Background(), SkillInstallRequest{
		ProjectPath:    project,
		Source:         source,
		Scope:          "project",
		ConflictPolicy: "skip",
	})
	require.NoError(t, err)

	// Skip policy should not overwrite the existing invalid SKILL.md.
	blob, readErr := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
	require.NoError(t, readErr)
	require.Equal(t, "invalid", string(blob))
}

func TestSkillInstaller_OverwriteFailureRollsBackExistingContent(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "rollback-source-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: rollback-source-skill
description: Skill
---
new content`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "extra.md"), []byte("new extra"), 0o644))

	targetDir := filepath.Join(project, ".reliant", "skills", "rollback-source-skill")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(`---
name: rollback-source-skill
description: Existing
---
old content`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "extra.md"), []byte("old extra"), 0o644))

	blockedRoot := filepath.Join(project, ".reliant", "skills", "forbidden")
	require.NoError(t, os.MkdirAll(blockedRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blockedRoot, "marker"), []byte("block"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(targetDir, "extra.md")))
	require.NoError(t, os.Symlink(blockedRoot, filepath.Join(targetDir, "extra.md")))

	service := NewSkillInstallerService()
	_, err := service.Install(context.Background(), SkillInstallRequest{
		ProjectPath:    project,
		Source:         source,
		Scope:          "project",
		ConflictPolicy: "overwrite",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")

	// Existing SKILL.md should remain unchanged after failed publish.
	blob, readErr := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
	require.NoError(t, readErr)
	require.Contains(t, string(blob), "description: Existing")
	require.Contains(t, string(blob), "old content")
}

func TestNormalizeInstallSourceSpec_GitHubTreeURL(t *testing.T) {
	source, subpath, ref, err := normalizeInstallSourceSpec(
		"https://github.com/anthropics/skills/tree/main/skills/frontend-design",
		"",
		"",
	)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/anthropics/skills.git", source)
	require.Equal(t, "skills/frontend-design", subpath)
	require.Equal(t, "main", ref)
}

func TestNormalizeInstallSourceSpec_GitHubTreeURL_ExplicitValuesWin(t *testing.T) {
	source, subpath, ref, err := normalizeInstallSourceSpec(
		"https://github.com/anthropics/skills/tree/main/skills/frontend-design",
		"skills/override",
		"release-1",
	)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/anthropics/skills.git", source)
	require.Equal(t, "skills/override", subpath)
	require.Equal(t, "release-1", ref)
}

func TestNormalizeInstallSourceSpec_GitHubBlobURL(t *testing.T) {
	source, subpath, ref, err := normalizeInstallSourceSpec(
		"https://github.com/anthropics/skills/blob/main/skills/frontend-design/SKILL.md",
		"",
		"",
	)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/anthropics/skills.git", source)
	require.Equal(t, "skills/frontend-design", subpath)
	require.Equal(t, "main", ref)
}

func TestNormalizeAndValidateGitSource_RejectsInsecureHTTP(t *testing.T) {
	_, err := normalizeAndValidateGitSource("http://example.com/repo.git")
	require.Error(t, err)
	require.Contains(t, err.Error(), "insecure git source protocol")
}

func TestNormalizeAndValidateGitSource_AllowsLocalhostHTTP(t *testing.T) {
	normalized, err := normalizeAndValidateGitSource("http://localhost/repo.git")
	require.NoError(t, err)
	require.Equal(t, "http://localhost/repo.git", normalized)
}

func TestNormalizeAndValidateGitSource_RejectsGitAtSCPStyle(t *testing.T) {
	_, err := normalizeAndValidateGitSource("git@github.com:owner/repo.git")
	require.Error(t, err)
	require.Contains(t, err.Error(), "scp-style")
}

func TestRedactSourceForDisplay_RemovesSensitiveCredentialsAndQueryValues(t *testing.T) {
	redacted := redactSourceForDisplay("https://user:secret-token@example.com/repo.git?token=abc123&ref=main")
	require.Contains(t, redacted, "https://user@example.com/repo.git")
	require.Contains(t, redacted, "token=%5BREDACTED%5D")
	require.NotContains(t, redacted, "secret-token")
	require.NotContains(t, redacted, "abc123")
}

func TestApplySourceSubpath_RejectsSymlinkEscape(t *testing.T) {
	baseDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, "safe"), 0o755))
	escapeTarget := t.TempDir()
	require.NoError(t, os.Symlink(escapeTarget, filepath.Join(baseDir, "safe", "link-out")))

	_, err := applySourceSubpath(baseDir, "safe/link-out")
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes source root")
}

func TestIsLikelyCommitSHA(t *testing.T) {
	require.True(t, isLikelyCommitSHA("0123456789abcdef0123456789abcdef01234567"))
	require.False(t, isLikelyCommitSHA("main"))
	require.False(t, isLikelyCommitSHA("0123456789abcdef0123456789abcdef0123456"))
	require.False(t, isLikelyCommitSHA("0123456789abcdef0123456789abcdef0123456z"))
}

func TestNormalizeInstallScope_InvalidValueRejected(t *testing.T) {
	_, err := normalizeInstallScope("invalid")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid scope")
}

func TestNormalizeConflictPolicy_InvalidValueRejected(t *testing.T) {
	_, err := normalizeConflictPolicy("not-a-policy")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid conflict_policy")
}

func TestSkillInstallerService_RejectsInvalidScopeAndConflictPolicy(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "strict-values-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: strict-values-skill
description: Strict values skill
---
Use it`), 0o644))

	service := NewSkillInstallerService()

	_, err := service.Install(context.Background(), SkillInstallRequest{
		ProjectPath: project,
		Source:      source,
		Scope:       "invalid",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid scope")

	_, err = service.Install(context.Background(), SkillInstallRequest{
		ProjectPath:    project,
		Source:         source,
		Scope:          "project",
		ConflictPolicy: "invalid",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid conflict_policy")
}

func TestResolveInstallSkillName_InvalidSkillDefinitionFails(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: Invalid_Name
description: bad
---
body`), 0o644))

	_, _, err := resolveInstallSkillName(source, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid SKILL.md")
}

func TestResolveInstallSkillName_MissingSkillDefinitionFails(t *testing.T) {
	source := t.TempDir()

	_, _, err := resolveInstallSkillName(source, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must contain SKILL.md")
}

func TestSkillInstaller_ConcurrentInstallsSameSkillAreSerialized(t *testing.T) {
	project := t.TempDir()
	source := filepath.Join(t.TempDir(), "concurrent-lock-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: concurrent-lock-skill
description: concurrent lock behavior
---
Use this skill`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "guide.md"), []byte("guide-v1"), 0o644))

	service := NewSkillInstallerService()

	targetPath := filepath.Join(project, ".reliant", "skills", "concurrent-lock-skill", "guide.md")

	installFn := func() error {
		_, err := service.Install(context.Background(), SkillInstallRequest{
			ProjectPath:    project,
			Source:         source,
			Scope:          "project",
			ConflictPolicy: "overwrite",
		})
		return err
	}

	errCh := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errCh <- installFn()
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	blob, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, "guide-v1", string(blob))
}

func createSkillRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "skills", "network-debug", "examples"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "skills", "network-debug", "SKILL.md"), []byte(`---
name: network-debug
description: Network debugging skill
---
Use this skill for network investigations.`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "skills", "network-debug", "examples", "playbook.md"), []byte("playbook"), 0o644))
	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	return repoDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}
