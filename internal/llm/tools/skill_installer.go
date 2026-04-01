// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/rctx"
	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

type InstallSkillParams struct {
	Source         string `json:"source" jsonschema:"required,description=Skill source. Supports local directory path and git URL (HTTPS/SSH/file://)."`
	SourceSubpath  string `json:"source_subpath,omitempty" jsonschema:"description=Optional subfolder inside source repository/directory where the skill lives."`
	Ref            string `json:"ref,omitempty" jsonschema:"description=Optional git ref (branch/tag/commit) when source is a git URL."`
	Name           string `json:"name,omitempty" jsonschema:"description=Optional destination skill name. Defaults to source folder name or parsed skill name."`
	Scope          string `json:"scope,omitempty" jsonschema:"description=Install scope: global | project | project_local (default: project)."`
	ConflictPolicy string `json:"conflict_policy,omitempty" jsonschema:"description=Conflict policy: skip | overwrite | rename (default: skip)."`
	DryRun         bool   `json:"dry_run,omitempty" jsonschema:"description=If true, preview what would be installed without writing files."`
}

type SkillInstallResult struct {
	Source         string   `json:"source"`
	SourceType     string   `json:"source_type"`
	SourceSubpath  string   `json:"source_subpath,omitempty"`
	GitRef         string   `json:"git_ref,omitempty"`
	ResolvedSource string   `json:"resolved_source"`
	TargetDir      string   `json:"target_dir"`
	SkillName      string   `json:"skill_name"`
	InstallDirName string   `json:"install_dir_name"`
	InstalledFiles []string `json:"installed_files"`
	SkippedFiles   []string `json:"skipped_files"`
	DryRun         bool     `json:"dry_run"`
	Scope          string   `json:"scope"`
	ConflictPolicy string   `json:"conflict_policy"`
}

// Current blockers:
//   - Uses exec.Command("git", "clone", ...) for cloning skill repos (cloneGitSource).
//   - Uses os.Stat, os.ReadFile, os.WriteFile, os.MkdirAll, os.RemoveAll for file operations.
//   - Uses filepath.Walk for recursive directory copying (copyDirRecursive).
//   - To migrate: use rctx.Daemon.RunCommand() for git clone, rctx.Daemon.ReadFile/WriteFile/
//     CreateDirectory/DeletePath for file ops, and rctx.Daemon.GlobFiles() to replace Walk.
//   - This tool is feature-flagged (skillsFeatureEnabled) and conditionally registered.
//   - Complex interaction between git clone into temp dir → validate → copy to target makes
//     migration non-trivial since temp dir operations also need daemon access.
type skillInstallerTool struct{}

const (
	SkillInstallerToolName = "install_skill"
	skillInstallerDesc     = `Install a skill into Reliant-managed skill paths.

WHEN TO USE:
- Install a local or git-hosted skill directory into project/local/global Reliant scope
- Preview what would be installed before writing files
- Apply conflict policy when destination files already exist

SOURCE SUPPORT:
- Local filesystem directory path
- Git URL (HTTPS/SSH/file://) with optional source_subpath
- GitHub tree/blob URL (auto-converted to clone URL + inferred ref/source_subpath)

DESTINATION SCOPES (Reliant-only):
- project: .reliant/skills
- project_local: .reliant.local/skills
- global: ~/.reliant/skills

CONFLICT POLICY:
- skip: keep existing files, skip conflicts (default)
- overwrite: replace existing files
- rename: install into an auto-suffixed skill directory name if target exists

SAFETY:
- Rejects path traversal and absolute source-escaping copies
- Refuses symlinked files/directories during copy
- Validates installed skill is discoverable/parseable

EXAMPLES:
{
  "source": "/tmp/my-skill",
  "scope": "project",
  "conflict_policy": "skip",
  "dry_run": true
}

{
  "source": "https://github.com/acme/skills.git",
  "source_subpath": "skills/incident-response",
  "ref": "main",
  "scope": "project_local",
  "conflict_policy": "rename",
  "dry_run": true
}`
)

var (
	safeSkillNameRegex        = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sensitiveSourceQueryKeyRe = regexp.MustCompile(`(?i)(token|key|secret|password|signature|auth)`)
)

func NewSkillInstallerTool() Tool {
	tool := &skillInstallerTool{}
	return NewToolWrapper[InstallSkillParams, ToolResponse](tool)
}

func FormatSkillInstallSummary(result SkillInstallResult) string {
	mode := "Installed"
	if result.DryRun {
		mode = "Dry-run preview"
	}
	return fmt.Sprintf("%s skill '%s' to %s\n- source_type: %s\n- source: %s\n- files to write: %d\n- skipped due to conflicts: %d\n- scope: %s\n- conflict_policy: %s",
		mode,
		result.SkillName,
		result.TargetDir,
		result.SourceType,
		result.ResolvedSource,
		len(result.InstalledFiles),
		len(result.SkippedFiles),
		result.Scope,
		result.ConflictPolicy,
	)
}

func (t *skillInstallerTool) Name() string { return SkillInstallerToolName }

func (t *skillInstallerTool) Description() string { return skillInstallerDesc }

func (t *skillInstallerTool) RequiresPermission(params InstallSkillParams) (bool, error) {
	if params.DryRun {
		return false, nil
	}
	return true, nil
}

func (t *skillInstallerTool) IsReadOnly() bool {
	return false
}

func (t *skillInstallerTool) Execute(r *rctx.ToolContext, params InstallSkillParams) (ToolResponse, error) {
	workingDir, err := GetWorkingDirectory(r)
	if err != nil {
		return NewTextErrorResponse("No project working directory available - ensure you're working within a project"), nil
	}

	service := NewSkillInstallerService()
	result, err := service.Install(r.Context, SkillInstallRequest{
		ProjectPath:    workingDir,
		Source:         params.Source,
		SourceSubpath:  params.SourceSubpath,
		Ref:            params.Ref,
		Name:           params.Name,
		Scope:          params.Scope,
		ConflictPolicy: params.ConflictPolicy,
		DryRun:         params.DryRun,
	})
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	return WithResponseMetadata(NewTextResponse(FormatSkillInstallSummary(result)), result), nil
}

func normalizeInstallSourceSpec(source, sourceSubpath, ref string) (normalizedSource string, normalizedSourceSubpath string, normalizedRef string, err error) {
	src := strings.TrimSpace(source)
	if src == "" {
		return "", "", "", fmt.Errorf("source is required")
	}
	resolvedSubpath := strings.TrimSpace(sourceSubpath)
	resolvedRef := strings.TrimSpace(ref)

	if cloneURL, inferredRef, inferredSubpath, ok := parseGitHubTreeLikeSource(src); ok {
		src = cloneURL
		if resolvedRef == "" {
			resolvedRef = inferredRef
		}
		if resolvedSubpath == "" {
			resolvedSubpath = inferredSubpath
		}
	}

	return src, resolvedSubpath, resolvedRef, nil
}

func parseGitHubTreeLikeSource(raw string) (cloneURL string, ref string, sourceSubpath string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", "", false
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return "", "", "", false
	}

	cleanPath := strings.Trim(strings.TrimSpace(u.Path), "/")
	if cleanPath == "" {
		return "", "", "", false
	}
	parts := strings.Split(cleanPath, "/")
	if len(parts) < 2 {
		return "", "", "", false
	}

	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	if owner == "" || repo == "" {
		return "", "", "", false
	}
	cloneURL = fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	if len(parts) < 4 {
		return "", "", "", false
	}
	mode := parts[2]
	if mode != "tree" && mode != "blob" {
		return "", "", "", false
	}

	decodedRef, err := url.PathUnescape(parts[3])
	if err != nil {
		decodedRef = parts[3]
	}
	if decodedRef == "" {
		return "", "", "", false
	}
	ref = decodedRef

	if len(parts) > 4 {
		subparts := make([]string, 0, len(parts)-4)
		for _, p := range parts[4:] {
			decoded, decodeErr := url.PathUnescape(p)
			if decodeErr != nil {
				decoded = p
			}
			subparts = append(subparts, decoded)
		}
		sourceSubpath = strings.Join(subparts, "/")
		if mode == "blob" {
			sourceSubpath = filepath.ToSlash(filepath.Dir(sourceSubpath))
			if sourceSubpath == "." {
				sourceSubpath = ""
			}
		}
	}

	return cloneURL, ref, sourceSubpath, true
}

func prepareInstallSource(source, ref string) (resolvedDir string, sourceType string, resolvedSource string, cleanup func(), err error) {
	src := strings.TrimSpace(source)
	if src == "" {
		return "", "", "", nil, fmt.Errorf("source is required")
	}

	if isLikelyGitSource(src) {
		normalizedSource, validateErr := normalizeAndValidateGitSource(src)
		if validateErr != nil {
			return "", "", "", nil, validateErr
		}
		normalizedRef := strings.TrimSpace(ref)
		tmpDir, cloneErr := cloneGitSource(normalizedSource, normalizedRef)
		if cloneErr != nil {
			return "", "", "", nil, cloneErr
		}
		resolved := redactSourceForDisplay(normalizedSource)
		if normalizedRef != "" {
			resolved = fmt.Sprintf("%s@%s", resolved, normalizedRef)
		}
		return tmpDir, "git", resolved, func() { _ = os.RemoveAll(tmpDir) }, nil
	}

	abs, absErr := filepath.Abs(src)
	if absErr != nil {
		return "", "", "", nil, fmt.Errorf("invalid source path: %v", absErr)
	}
	st, statErr := os.Stat(abs)
	if statErr != nil {
		return "", "", "", nil, fmt.Errorf("source path not accessible: %v", statErr)
	}
	if !st.IsDir() {
		return "", "", "", nil, fmt.Errorf("source must be a directory")
	}
	return abs, "local", abs, nil, nil
}

func isLikelyGitSource(source string) bool {
	s := strings.TrimSpace(source)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "git@") {
		return true
	}
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "ssh://") || strings.HasPrefix(s, "file://") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(s), ".git")
}

func normalizeAndValidateGitSource(raw string) (string, error) {
	source := strings.TrimSpace(raw)
	if source == "" {
		return "", fmt.Errorf("source is required")
	}

	if strings.HasPrefix(source, "git@") {
		return "", fmt.Errorf("git SSH scp-style sources are not supported; use https://, ssh://, or file://")
	}

	u, err := url.Parse(source)
	if err != nil {
		return "", fmt.Errorf("invalid git source URL: %v", err)
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch scheme {
	case "https", "ssh":
		if host == "" {
			return "", fmt.Errorf("git source host is required for %s URLs", scheme)
		}
	case "file":
		if host != "" && host != "localhost" {
			return "", fmt.Errorf("file:// git source must use localhost or empty host")
		}
	case "http":
		if host == "localhost" || host == "127.0.0.1" {
			return source, nil
		}
		return "", fmt.Errorf("insecure git source protocol is not allowed: http")
	default:
		return "", fmt.Errorf("unsupported git source protocol: %s", scheme)
	}

	return source, nil
}

func redactSourceForDisplay(source string) string {
	u, err := url.Parse(source)
	if err != nil {
		return source
	}

	if u.User != nil {
		username := u.User.Username()
		if username != "" {
			u.User = url.User(username)
		} else {
			u.User = nil
		}
	}

	query := u.Query()
	if len(query) > 0 {
		for key := range query {
			if sensitiveSourceQueryKeyRe.MatchString(key) {
				query.Set(key, "[REDACTED]")
			}
		}
		u.RawQuery = query.Encode()
	}

	return u.String()
}

func cloneGitSource(source, ref string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "reliant-skill-install-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory for git source: %w", err)
	}
	cleanupOnError := func(inErr error) (string, error) {
		_ = os.RemoveAll(tmpDir)
		return "", inErr
	}

	normalizedRef := strings.TrimSpace(ref)
	cloneArgs := []string{"clone", "--quiet"}
	if normalizedRef == "" || isLikelyCommitSHA(normalizedRef) {
		cloneArgs = append(cloneArgs, "--depth", "1")
	}
	cloneArgs = append(cloneArgs, source, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cloneCmd := exec.CommandContext(ctx, "git", cloneArgs...)
	if out, cloneErr := cloneCmd.CombinedOutput(); cloneErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return cleanupOnError(fmt.Errorf("failed to clone git source: command timed out"))
		}
		return cleanupOnError(fmt.Errorf("failed to clone git source %q: %v: %s", redactSourceForDisplay(source), cloneErr, strings.TrimSpace(string(out))))
	}

	if normalizedRef != "" {
		checkoutCtx, checkoutCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer checkoutCancel()
		checkoutCmd := exec.CommandContext(checkoutCtx, "git", "-C", tmpDir, "checkout", "--quiet", normalizedRef)
		if out, checkoutErr := checkoutCmd.CombinedOutput(); checkoutErr != nil {
			if checkoutCtx.Err() == context.DeadlineExceeded {
				return cleanupOnError(fmt.Errorf("failed to checkout git ref %q: command timed out", normalizedRef))
			}
			return cleanupOnError(fmt.Errorf("failed to checkout git ref %q: %v: %s", normalizedRef, checkoutErr, strings.TrimSpace(string(out))))
		}

		if isLikelyCommitSHA(normalizedRef) {
			headCtx, headCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer headCancel()
			headCmd := exec.CommandContext(headCtx, "git", "-C", tmpDir, "rev-parse", "HEAD")
			headOut, headErr := headCmd.CombinedOutput()
			if headErr != nil {
				if headCtx.Err() == context.DeadlineExceeded {
					return cleanupOnError(fmt.Errorf("failed to verify checked out commit %q: command timed out", normalizedRef))
				}
				return cleanupOnError(fmt.Errorf("failed to verify checked out commit %q: %v: %s", normalizedRef, headErr, strings.TrimSpace(string(headOut))))
			}
			resolvedHead := strings.TrimSpace(string(headOut))
			if !strings.EqualFold(resolvedHead, normalizedRef) {
				return cleanupOnError(fmt.Errorf("requested commit %q but checked out %q", normalizedRef, resolvedHead))
			}
		}
	}

	return tmpDir, nil
}

func applySourceSubpath(baseDir, sourceSubpath string) (string, error) {
	sub := strings.TrimSpace(sourceSubpath)
	if sub == "" || sub == "." {
		return baseDir, nil
	}
	cleanSub := filepath.Clean(sub)
	if filepath.IsAbs(cleanSub) || cleanSub == ".." || strings.HasPrefix(cleanSub, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid source_subpath: %s", sourceSubpath)
	}

	candidate := filepath.Join(baseDir, cleanSub)
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve source base: %v", err)
	}
	candAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("failed to resolve source_subpath: %v", err)
	}
	rel, err := filepath.Rel(baseAbs, candAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source_subpath escapes source root")
	}

	baseEval, err := filepath.EvalSymlinks(baseAbs)
	if err != nil {
		return "", fmt.Errorf("failed to resolve source root symlinks: %v", err)
	}
	candEval, err := filepath.EvalSymlinks(candAbs)
	if err != nil {
		return "", fmt.Errorf("failed to resolve source_subpath symlinks: %v", err)
	}
	if !isWithinRoot(baseEval, candEval) {
		return "", fmt.Errorf("source_subpath escapes source root")
	}

	st, err := os.Stat(candEval)
	if err != nil {
		return "", fmt.Errorf("source_subpath not accessible: %v", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("source_subpath must resolve to a directory")
	}
	return candEval, nil
}

type copyPlanEntry struct {
	Rel     string
	Src     string
	Dst     string
	Content []byte
}

const (
	SkillInstallScopeProject      = "project"
	SkillInstallScopeProjectLocal = "project_local"
	SkillInstallScopeGlobal       = "global"

	SkillInstallConflictSkip      = "skip"
	SkillInstallConflictOverwrite = "overwrite"
	SkillInstallConflictRename    = "rename"
)

type SkillInstallRequest struct {
	ProjectPath    string
	Source         string
	SourceSubpath  string
	Ref            string
	Name           string
	Scope          string
	ConflictPolicy string
	DryRun         bool
}

type SkillInstallerService struct{}

func NewSkillInstallerService() *SkillInstallerService {
	return &SkillInstallerService{}
}

type stagedInstallPlan struct {
	Root      string
	TargetDir string
	Entries   []copyPlanEntry
}

type targetDirPreparation struct {
	Exists bool
}

func (s *SkillInstallerService) Install(_ context.Context, req SkillInstallRequest) (SkillInstallResult, error) {
	if strings.TrimSpace(req.ProjectPath) == "" {
		return SkillInstallResult{}, fmt.Errorf("project path is required")
	}
	if strings.TrimSpace(req.Source) == "" {
		return SkillInstallResult{}, fmt.Errorf("source is required")
	}

	normalizedSource, normalizedSourceSubpath, normalizedRef, err := normalizeInstallSourceSpec(req.Source, req.SourceSubpath, req.Ref)
	if err != nil {
		return SkillInstallResult{}, err
	}

	resolvedSourceDir, sourceType, resolvedSource, cleanup, err := prepareInstallSource(normalizedSource, normalizedRef)
	if err != nil {
		return SkillInstallResult{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	resolvedSourceDir, err = applySourceSubpath(resolvedSourceDir, normalizedSourceSubpath)
	if err != nil {
		return SkillInstallResult{}, err
	}

	scope, err := normalizeInstallScope(req.Scope)
	if err != nil {
		return SkillInstallResult{}, err
	}
	targetRoot, err := resolveSkillInstallRoot(req.ProjectPath, scope)
	if err != nil {
		return SkillInstallResult{}, err
	}
	trustedBase, err := resolveSkillInstallTrustedBase(req.ProjectPath, scope)
	if err != nil {
		return SkillInstallResult{}, err
	}

	skillName, sourceDefinitionFormat, err := resolveInstallSkillName(resolvedSourceDir, req.Name)
	if err != nil {
		return SkillInstallResult{}, err
	}

	conflictPolicy, err := normalizeConflictPolicy(req.ConflictPolicy)
	if err != nil {
		return SkillInstallResult{}, err
	}

	installDirName := skillName
	targetDir := filepath.Join(targetRoot, installDirName)

	if conflictPolicy == SkillInstallConflictRename {
		targetDir, installDirName, err = allocateRenamedTargetDir(targetRoot, installDirName)
		if err != nil {
			return SkillInstallResult{}, fmt.Errorf("failed to allocate rename target: %v", err)
		}
		if sourceDefinitionFormat == skillscore.SkillFormatClaudeMarkdown && installDirName != skillName {
			return SkillInstallResult{}, fmt.Errorf("rename conflict policy is not supported for SKILL.md skills because Agent Skills require name to match parent directory; use skip or overwrite")
		}
	}

	if !req.DryRun {
		if err := ensureInstallRoot(targetRoot, trustedBase); err != nil {
			return SkillInstallResult{}, err
		}
		releaseLock, lockErr := acquireInstallLock(targetRoot, installDirName)
		if lockErr != nil {
			return SkillInstallResult{}, lockErr
		}
		defer releaseLock()
	}

	plan, err := buildCopyPlan(resolvedSourceDir, targetDir)
	if err != nil {
		return SkillInstallResult{}, fmt.Errorf("failed to build install plan: %v", err)
	}
	if len(plan) == 0 {
		return SkillInstallResult{}, fmt.Errorf("source skill directory is empty or contains no installable files")
	}

	installed := make([]string, 0, len(plan))
	skipped := []string{}

	if req.DryRun {
		for _, p := range plan {
			if _, err := os.Stat(p.Dst); err == nil && conflictPolicy == SkillInstallConflictSkip {
				skipped = append(skipped, p.Rel)
				continue
			}
			installed = append(installed, p.Rel)
		}
	} else {
		preparedTarget, prepErr := prepareTargetDirectory(trustedBase, targetDir)
		if prepErr != nil {
			return SkillInstallResult{}, prepErr
		}

		staged, stagedErr := stageInstallPlan(trustedBase, targetRoot, plan, conflictPolicy)
		if stagedErr != nil {
			return SkillInstallResult{}, stagedErr
		}
		defer func() { _ = os.RemoveAll(staged.Root) }()

		installed = append(installed, stagedRelativePaths(staged.Entries)...)
		if conflictPolicy == SkillInstallConflictSkip {
			skipped = collectSkippedPaths(plan, staged.Entries)
		}

		if publishErr := publishStagedInstall(trustedBase, staged, preparedTarget, targetDir); publishErr != nil {
			return SkillInstallResult{}, publishErr
		}

		validateInstalledSkill := false
		for _, entry := range staged.Entries {
			if entry.Rel == "SKILL.md" {
				validateInstalledSkill = true
				break
			}
		}
		if validateInstalledSkill {
			installedSkillPath := filepath.Join(targetDir, "SKILL.md")
			if _, statErr := os.Stat(installedSkillPath); statErr != nil {
				return SkillInstallResult{}, fmt.Errorf("install completed but validation could not read installed SKILL.md: %v", statErr)
			}
			blob, readErr := os.ReadFile(installedSkillPath)
			if readErr != nil {
				return SkillInstallResult{}, fmt.Errorf("install completed but validation could not read installed SKILL.md: %v", readErr)
			}
			if _, parseErr := skillcatalog.ParseSkillMarkdown(installedSkillPath, skillscore.ScopeProject, blob); parseErr != nil {
				return SkillInstallResult{}, fmt.Errorf("install completed but installed SKILL.md is invalid: %v", parseErr)
			}
		}
	}

	sort.Strings(installed)
	sort.Strings(skipped)

	if !req.DryRun {
		skillcatalog.DefaultCatalogIndex().Invalidate(req.ProjectPath)
	}

	return SkillInstallResult{
		Source:         req.Source,
		SourceType:     sourceType,
		SourceSubpath:  normalizedSourceSubpath,
		GitRef:         normalizedRef,
		ResolvedSource: resolvedSource,
		TargetDir:      targetDir,
		SkillName:      skillName,
		InstallDirName: installDirName,
		InstalledFiles: installed,
		SkippedFiles:   skipped,
		DryRun:         req.DryRun,
		Scope:          scope,
		ConflictPolicy: conflictPolicy,
	}, nil
}

func normalizeInstallScope(scope string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(scope))
	switch value {
	case "", SkillInstallScopeProject:
		return SkillInstallScopeProject, nil
	case SkillInstallScopeProjectLocal, "local":
		return SkillInstallScopeProjectLocal, nil
	case SkillInstallScopeGlobal, "user":
		return SkillInstallScopeGlobal, nil
	default:
		return "", fmt.Errorf("invalid scope %q: expected one of project | project_local | global", scope)
	}
}

func normalizeConflictPolicy(policy string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(policy))
	switch value {
	case "", SkillInstallConflictSkip:
		return SkillInstallConflictSkip, nil
	case SkillInstallConflictOverwrite:
		return SkillInstallConflictOverwrite, nil
	case SkillInstallConflictRename:
		return SkillInstallConflictRename, nil
	default:
		return "", fmt.Errorf("invalid conflict_policy %q: expected one of skip | overwrite | rename", policy)
	}
}

func resolveSkillInstallRoot(projectPath, scope string) (string, error) {
	switch scope {
	case SkillInstallScopeProject:
		return filepath.Join(projectPath, config.ReliantDir, "skills"), nil
	case SkillInstallScopeProjectLocal:
		return filepath.Join(projectPath, config.ReliantLocalDir, "skills"), nil
	case SkillInstallScopeGlobal:
		return filepath.Join(config.GetUserConfigDir(), "skills"), nil
	default:
		return "", fmt.Errorf("unsupported scope: %s", scope)
	}
}

func resolveInstallSkillName(sourceDir, overrideName string) (string, skillscore.SkillFormat, error) {
	if strings.TrimSpace(overrideName) != "" {
		name := strings.TrimSpace(overrideName)
		if len(name) > 64 || !safeSkillNameRegex.MatchString(name) {
			return "", "", fmt.Errorf("invalid skill name override: %s", name)
		}
		return name, "", nil
	}

	p := filepath.Join(sourceDir, "SKILL.md")
	if blob, err := os.ReadFile(p); err == nil {
		s, parseErr := skillcatalog.ParseSkillMarkdown(p, skillscore.ScopeProject, blob)
		if parseErr == nil {
			return s.Name, skillscore.SkillFormatClaudeMarkdown, nil
		}
		return "", "", fmt.Errorf("invalid SKILL.md in source: %w", parseErr)
	}

	return "", "", fmt.Errorf("source skill directory must contain SKILL.md")
}

func prepareTargetDirectory(trustedBase, targetDir string) (targetDirPreparation, error) {
	parent := filepath.Dir(targetDir)
	if err := ensurePathNoSymlinksWithinRoot(trustedBase, parent); err != nil {
		return targetDirPreparation{}, err
	}

	info, err := os.Lstat(targetDir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return targetDirPreparation{}, fmt.Errorf("destination path is a symlink: %s", targetDir)
		}
		if !info.IsDir() {
			return targetDirPreparation{}, fmt.Errorf("destination path is not a directory: %s", targetDir)
		}
		if err := ensurePathNoSymlinksWithinRoot(trustedBase, targetDir); err != nil {
			return targetDirPreparation{}, err
		}
		return targetDirPreparation{Exists: true}, nil
	}
	if !os.IsNotExist(err) {
		return targetDirPreparation{}, fmt.Errorf("failed to inspect destination path: %v", err)
	}
	return targetDirPreparation{Exists: false}, nil
}

func stageInstallPlan(trustedBase, targetRoot string, plan []copyPlanEntry, conflictPolicy string) (stagedInstallPlan, error) {
	stageRoot, err := os.MkdirTemp(targetRoot, ".skill-install-stage-*")
	if err != nil {
		return stagedInstallPlan{}, fmt.Errorf("failed to create staging directory: %v", err)
	}

	stagedEntries := make([]copyPlanEntry, 0, len(plan))
	for _, p := range plan {
		relPath := filepath.FromSlash(p.Rel)
		stagedDst := filepath.Join(stageRoot, relPath)
		liveDst := p.Dst

		exists := false
		if _, statErr := os.Stat(liveDst); statErr == nil {
			exists = true
		} else if !os.IsNotExist(statErr) {
			_ = os.RemoveAll(stageRoot)
			return stagedInstallPlan{}, fmt.Errorf("failed to check destination file %s: %v", p.Rel, statErr)
		}
		if exists {
			if conflictPolicy == SkillInstallConflictSkip {
				continue
			}
			if err := rejectSymlinkTargetWithinRoot(trustedBase, liveDst); err != nil {
				_ = os.RemoveAll(stageRoot)
				return stagedInstallPlan{}, err
			}
		}

		if err := ensurePathNoSymlinksWithinRoot(trustedBase, filepath.Dir(liveDst)); err != nil {
			_ = os.RemoveAll(stageRoot)
			return stagedInstallPlan{}, err
		}
		if err := os.MkdirAll(filepath.Dir(stagedDst), 0o755); err != nil {
			_ = os.RemoveAll(stageRoot)
			return stagedInstallPlan{}, fmt.Errorf("failed to create staging directory for %s: %v", p.Rel, err)
		}
		if err := os.WriteFile(stagedDst, p.Content, 0o644); err != nil {
			_ = os.RemoveAll(stageRoot)
			return stagedInstallPlan{}, fmt.Errorf("failed writing staged file %s: %v", p.Rel, err)
		}
		stagedEntries = append(stagedEntries, copyPlanEntry{Rel: p.Rel, Src: p.Src, Dst: liveDst, Content: p.Content})
	}

	return stagedInstallPlan{Root: stageRoot, Entries: stagedEntries}, nil
}

type publishedEntryState struct {
	Dst       string
	Backup    string
	HadBackup bool
}

func publishStagedInstall(trustedBase string, staged stagedInstallPlan, prepared targetDirPreparation, targetDir string) error {
	if len(staged.Entries) == 0 {
		return nil
	}

	if !prepared.Exists {
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return fmt.Errorf("failed to create destination directory: %v", err)
		}
	}

	applied := make([]publishedEntryState, 0, len(staged.Entries))
	rollbackAndWrap := func(cause error) error {
		if rollbackErr := rollbackPublishedEntries(applied); rollbackErr != nil {
			return fmt.Errorf("%w (rollback failed: %v)", cause, rollbackErr)
		}
		return cause
	}

	for _, entry := range staged.Entries {
		relPath := filepath.FromSlash(entry.Rel)
		stagedPath := filepath.Join(staged.Root, relPath)
		if err := ensurePathNoSymlinksWithinRoot(trustedBase, filepath.Dir(entry.Dst)); err != nil {
			return rollbackAndWrap(err)
		}
		if err := rejectSymlinkTargetWithinRoot(trustedBase, entry.Dst); err != nil {
			return rollbackAndWrap(err)
		}
		if err := os.MkdirAll(filepath.Dir(entry.Dst), 0o755); err != nil {
			return rollbackAndWrap(fmt.Errorf("failed to create destination directory: %v", err))
		}

		state := publishedEntryState{Dst: entry.Dst}
		if existingInfo, err := os.Lstat(entry.Dst); err == nil {
			if existingInfo.IsDir() {
				return rollbackAndWrap(fmt.Errorf("destination path is a directory: %s", entry.Dst))
			}
			backupPath := filepath.Join(staged.Root, ".rollback", relPath)
			if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
				return rollbackAndWrap(fmt.Errorf("failed to prepare rollback path for %s: %v", entry.Rel, err))
			}
			if err := os.Rename(entry.Dst, backupPath); err != nil {
				return rollbackAndWrap(fmt.Errorf("failed to back up destination file %s: %v", entry.Rel, err))
			}
			state.Backup = backupPath
			state.HadBackup = true
		} else if !os.IsNotExist(err) {
			return rollbackAndWrap(fmt.Errorf("failed to inspect destination file %s: %v", entry.Rel, err))
		}

		if err := os.Rename(stagedPath, entry.Dst); err != nil {
			blob, readErr := os.ReadFile(stagedPath)
			if readErr != nil {
				if state.HadBackup {
					_ = os.Rename(state.Backup, entry.Dst)
				}
				return rollbackAndWrap(fmt.Errorf("failed to read staged file %s: %v", entry.Rel, readErr))
			}
			if writeErr := os.WriteFile(entry.Dst, blob, 0o644); writeErr != nil {
				if state.HadBackup {
					_ = os.Rename(state.Backup, entry.Dst)
				}
				return rollbackAndWrap(fmt.Errorf("failed writing destination file %s: %v", entry.Rel, writeErr))
			}
			_ = os.Remove(stagedPath)
		}

		applied = append(applied, state)
	}

	_ = os.RemoveAll(filepath.Join(staged.Root, ".rollback"))
	return nil
}

func rollbackPublishedEntries(applied []publishedEntryState) error {
	var rollbackErrs []string
	for i := len(applied) - 1; i >= 0; i-- {
		entry := applied[i]
		if removeErr := os.Remove(entry.Dst); removeErr != nil && !os.IsNotExist(removeErr) {
			rollbackErrs = append(rollbackErrs, fmt.Sprintf("remove %s: %v", entry.Dst, removeErr))
		}
		if entry.HadBackup {
			if err := os.MkdirAll(filepath.Dir(entry.Dst), 0o755); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Sprintf("mkdir restore dir for %s: %v", entry.Dst, err))
				continue
			}
			if err := os.Rename(entry.Backup, entry.Dst); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Sprintf("restore %s: %v", entry.Dst, err))
			}
		}
	}
	if len(rollbackErrs) > 0 {
		return fmt.Errorf("%s", strings.Join(rollbackErrs, "; "))
	}
	return nil
}

func stagedRelativePaths(entries []copyPlanEntry) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Rel)
	}
	return result
}

func collectSkippedPaths(plan []copyPlanEntry, staged []copyPlanEntry) []string {
	stagedSet := make(map[string]struct{}, len(staged))
	for _, entry := range staged {
		stagedSet[entry.Rel] = struct{}{}
	}
	var skipped []string
	for _, entry := range plan {
		if _, ok := stagedSet[entry.Rel]; ok {
			continue
		}
		skipped = append(skipped, entry.Rel)
	}
	return skipped
}

func resolveSkillInstallTrustedBase(projectPath, scope string) (string, error) {
	switch scope {
	case SkillInstallScopeProject:
		return filepath.Clean(filepath.Join(projectPath, config.ReliantDir)), nil
	case SkillInstallScopeProjectLocal:
		return filepath.Clean(filepath.Join(projectPath, config.ReliantLocalDir)), nil
	case SkillInstallScopeGlobal:
		return filepath.Clean(config.GetUserConfigDir()), nil
	default:
		return "", fmt.Errorf("unsupported scope: %s", scope)
	}
}

func ensureInstallRoot(targetRoot, trustedBase string) error {
	cleanTrustedBase := filepath.Clean(trustedBase)
	cleanTargetRoot := filepath.Clean(targetRoot)
	if !isWithinRoot(cleanTrustedBase, cleanTargetRoot) {
		return fmt.Errorf("install root escapes trusted base")
	}

	trustedInfo, err := os.Lstat(cleanTrustedBase)
	if err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(cleanTrustedBase, 0o755); mkErr != nil {
				return fmt.Errorf("failed to create trusted install base: %v", mkErr)
			}
		} else {
			return fmt.Errorf("failed to inspect trusted install base: %v", err)
		}
	} else if trustedInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("trusted install base is a symlink: %s", cleanTrustedBase)
	}

	if err := os.MkdirAll(cleanTargetRoot, 0o755); err != nil {
		return fmt.Errorf("failed to create install root: %v", err)
	}

	if err := ensurePathNoSymlinksWithinRoot(cleanTrustedBase, cleanTargetRoot); err != nil {
		return err
	}
	return nil
}

func acquireInstallLock(targetRoot, installDirName string) (func(), error) {
	lockDir := filepath.Join(targetRoot, ".locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %v", err)
	}
	lockName := fmt.Sprintf("%s.lock", installDirName)
	lockPath := filepath.Join(lockDir, lockName)
	for i := 0; i < 40; i++ {
		f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("failed to acquire install lock: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for install lock")
}

func ensurePathNoSymlinksWithinRoot(trustedRoot, path string) error {
	cleanRoot := filepath.Clean(trustedRoot)
	cleanPath := filepath.Clean(path)
	if !isWithinRoot(cleanRoot, cleanPath) {
		return fmt.Errorf("destination path escapes trusted base")
	}

	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return fmt.Errorf("failed to compute relative destination path: %v", err)
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("destination path escapes trusted base")
	}

	curr := cleanRoot
	parts := strings.Split(rel, string(os.PathSeparator))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		curr = filepath.Join(curr, part)
		info, lstatErr := os.Lstat(curr)
		if lstatErr != nil {
			if os.IsNotExist(lstatErr) {
				break
			}
			return fmt.Errorf("failed to inspect destination path %s: %v", curr, lstatErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path contains symlink component: %s", curr)
		}
	}
	return nil
}

func rejectSymlinkTargetWithinRoot(trustedRoot, path string) error {
	if err := ensurePathNoSymlinksWithinRoot(trustedRoot, filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to inspect destination file %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination file is a symlink: %s", path)
	}
	return nil
}

func isWithinRoot(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if candidate == root {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func allocateRenamedTargetDir(targetRoot, requestedName string) (string, string, error) {
	base := requestedName
	candidate := filepath.Join(targetRoot, base)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate, base, nil
	}
	for i := 2; i < 1000; i++ {
		name := fmt.Sprintf("%s-%d", base, i)
		candidate = filepath.Join(targetRoot, name)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, name, nil
		}
	}
	return "", "", fmt.Errorf("unable to allocate unique renamed destination")
}

func isLikelyCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, ch := range ref {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}

func buildCopyPlan(sourceDir, targetDir string) ([]copyPlanEntry, error) {
	var plan []copyPlanEntry
	err := filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in skill source: %s", path)
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("invalid relative path in source: %s", rel)
		}

		relSlash := filepath.ToSlash(rel)

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(targetDir, rel)
		plan = append(plan, copyPlanEntry{Rel: relSlash, Src: path, Dst: dst, Content: content})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}
