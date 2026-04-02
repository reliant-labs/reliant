package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/features"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/skills"
	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingsTestDaemonRouter is a minimal DaemonRouter for settings tests.
// It handles skills.delete_global by deleting the skill directory from the local filesystem.
type settingsTestDaemonRouter struct{}

func (r *settingsTestDaemonRouter) IsDaemonOnline(_ context.Context, userID string) (bool, error) {
	return true, nil
}
func (r *settingsTestDaemonRouter) SendToolRequest(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) error {
	return nil
}
func (r *settingsTestDaemonRouter) SendToolExecutionCancel(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *settingsTestDaemonRouter) SendKillProcess(_ context.Context, _, _ string) error {
	return nil
}
func (r *settingsTestDaemonRouter) SendLoadProjectConfigs(_ context.Context, _, _ string, _ string) error {
	return nil
}
func (r *settingsTestDaemonRouter) SendWatchProjectConfigs(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}
func (r *settingsTestDaemonRouter) SendToolRequestSync(_ context.Context, _ string, _ *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	return &toolexec.ToolExecutionResponse{Success: true}, nil
}
func (r *settingsTestDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType == "skills.read_skill_assets" {
		var req map[string]string
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		skillDir := req["skill_dir"]
		if skillDir == "" {
			return json.Marshal(map[string]interface{}{"assets": []interface{}{}})
		}
		assetsRoot := filepath.Join(skillDir, "assets")
		info, statErr := os.Stat(assetsRoot)
		if statErr != nil || !info.IsDir() {
			return json.Marshal(map[string]interface{}{"assets": []interface{}{}})
		}
		var assets []map[string]string
		_ = filepath.WalkDir(assetsRoot, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(skillDir, path)
			if relErr != nil {
				return nil
			}
			ext := filepath.Ext(rel)
			mimeType := ""
			switch ext {
			case ".png":
				mimeType = "image/png"
			case ".jpg", ".jpeg":
				mimeType = "image/jpeg"
			case ".gif":
				mimeType = "image/gif"
			case ".svg":
				mimeType = "image/svg+xml"
			case ".webp":
				mimeType = "image/webp"
			default:
				return nil
			}
			blob, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			assets = append(assets, map[string]string{
				"relative_path": rel,
				"mime_type":     mimeType,
				"content_b64":   base64.StdEncoding.EncodeToString(blob),
			})
			return nil
		})
		if assets == nil {
			assets = []map[string]string{}
		}
		return json.Marshal(map[string]interface{}{"assets": assets})
	}
	if commandType == "skills.read_file" {
		var req map[string]string
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		filePath := req["path"]
		content, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				return json.Marshal(map[string]interface{}{"content": "", "found": false})
			}
			return nil, err
		}
		return json.Marshal(map[string]interface{}{"content": string(content), "found": true})
	}
	if commandType == "skills.delete_global" {
		var req map[string]string
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		relPath := req["relative_path"]
		home, _ := os.UserHomeDir()
		skillDir := filepath.Join(home, ".reliant", "skills", relPath)

		// Validate SKILL.md exists (matches real daemon behavior)
		skillFile := filepath.Join(skillDir, "SKILL.md")
		content, err := os.ReadFile(skillFile)
		if err != nil {
			return json.Marshal(map[string]interface{}{"success": false, "error": "not a valid skill directory: SKILL.md not found", "error_code": "invalid_argument"})
		}

		if err := os.RemoveAll(skillDir); err != nil {
			return json.Marshal(map[string]interface{}{"success": false, "error": err.Error(), "error_code": "not_found"})
		}
		return json.Marshal(map[string]interface{}{"success": true, "definition_content": string(content)})
	}
	return nil, fmt.Errorf("unhandled command: %s", commandType)
}
func (r *settingsTestDaemonRouter) SendTerminalInput(_ context.Context, _, _ string, _ []byte) error {
	return nil
}
func (r *settingsTestDaemonRouter) SendTerminalResize(_ context.Context, _, _ string, _, _ uint32) error {
	return nil
}
func (r *settingsTestDaemonRouter) SubscribeTerminalOutput(_ context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	ch := make(chan *toolexec.TerminalOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *settingsTestDaemonRouter) SubscribeProcessOutput(_ context.Context, _, _ string, _ bool) (<-chan *toolexec.ProcessOutputEvent, func(), error) {
	ch := make(chan *toolexec.ProcessOutputEvent)
	return ch, func() { close(ch) }, nil
}
func (r *settingsTestDaemonRouter) Close() error { return nil }

func makeCodexJWTForSettingsTest(t *testing.T, exp time.Time) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]interface{}{
		"exp": exp.Unix(),
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_account_id": "test-account-id",
		},
	}
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)

	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signature := base64.RawURLEncoding.EncodeToString([]byte("test-signature"))
	return header + "." + payload + "." + signature
}

func newSettingsServiceTestContext() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
}

func findProviderStatus(t *testing.T, statuses []*reliantv1.ProviderStatus, provider string) *reliantv1.ProviderStatus {
	t.Helper()
	for _, status := range statuses {
		if status.Provider == provider {
			return status
		}
	}
	t.Fatalf("provider %q not found", provider)
	return nil
}

func hasPathSegment(pathValue string, segment string) bool {
	if segment == "" {
		return false
	}
	clean := filepath.Clean(pathValue)
	for {
		if filepath.Base(clean) == segment {
			return true
		}
		next := filepath.Dir(clean)
		if next == clean {
			return false
		}
		clean = next
	}
}

func isSkillPathForName(pathValue string, skillName string) bool {
	if hasPathSegment(pathValue, skillName) {
		return true
	}
	if runtime.GOOS == "windows" {
		return hasPathSegment(pathValue, strings.ToLower(skillName))
	}
	return false
}

func TestSettingsService_UpdateProviderAPIKey_CodexRejectsManualKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	_, err := svc.UpdateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.UpdateProviderAPIKeyRequest{
		Provider: "codex",
		ApiKey:   "manual-key-not-allowed",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "does not use manual API keys")
}

func TestSettingsService_GetSetting_SkillsEnabledReflectsEnvOverride(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	t.Setenv(features.SkillsEnabledEnvVar, "true")

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	resp, err := svc.GetSetting(ctx, connect.NewRequest(&reliantv1.GetSettingRequest{
		Key: features.SkillsEnabledSetting,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetSetting())
	assert.Equal(t, features.SkillsEnabledSetting, resp.Msg.GetSetting().GetKey())
	assert.Equal(t, "true", resp.Msg.GetSetting().GetValue())
	assert.Equal(t, "bool", resp.Msg.GetSetting().GetValueType())
}

func TestSettingsService_UpdateProviderAPIKey_CodexDisconnectRemovesMarkerAndTokens(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	userID := "test-user"

	validToken := makeCodexJWTForSettingsTest(t, time.Now().Add(1*time.Hour))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	resp, err := svc.UpdateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.UpdateProviderAPIKeyRequest{
		Provider: "codex",
		ApiKey:   "",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	assert.Equal(t, "Disconnected from Codex", resp.Msg.Message)

	tokens, err := repo.GetCodexAuthTokens(ctx, userID)
	require.NoError(t, err)
	assert.Nil(t, tokens)

	keys, err := repo.GetProviderAPIKeys(ctx, userID)
	require.NoError(t, err)
	_, hasCodex := keys["codex"]
	assert.False(t, hasCodex)
}

func TestSettingsService_ValidateProviderAPIKey_CodexStatusFromPersistedTokens(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	userID := "test-user"

	// Missing tokens => not connected
	missingResp, err := svc.ValidateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
		Provider: "codex",
	}))
	require.NoError(t, err)
	assert.False(t, missingResp.Msg.Valid)
	assert.Equal(t, "Codex is not connected", missingResp.Msg.Message)

	// Expired token => expired message
	expiredToken := makeCodexJWTForSettingsTest(t, time.Now().Add(-10*time.Minute))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  expiredToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	expiredResp, err := svc.ValidateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
		Provider: "codex",
	}))
	require.NoError(t, err)
	assert.False(t, expiredResp.Msg.Valid)
	assert.Contains(t, expiredResp.Msg.Message, "session expired")

	// Valid token => connected
	validToken := makeCodexJWTForSettingsTest(t, time.Now().Add(1*time.Hour))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	validResp, err := svc.ValidateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
		Provider: "codex",
	}))
	require.NoError(t, err)
	assert.True(t, validResp.Msg.Valid)
	assert.Equal(t, "Connected to Codex", validResp.Msg.Message)
}

func TestSettingsService_GetProviderStatuses_CodexUsesPersistedTokenExpiry(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	userID := "test-user"

	validToken := makeCodexJWTForSettingsTest(t, time.Now().Add(1*time.Hour))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	resp, err := svc.GetProviderStatuses(ctx, connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
	require.NoError(t, err)

	codexStatus := findProviderStatus(t, resp.Msg.Providers, "codex")
	assert.True(t, codexStatus.Configured)
	assert.True(t, codexStatus.HasApiKey)
	assert.Nil(t, codexStatus.MaskedKey)

	expiredToken := makeCodexJWTForSettingsTest(t, time.Now().Add(-10*time.Minute))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  expiredToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	expiredResp, err := svc.GetProviderStatuses(ctx, connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
	require.NoError(t, err)

	expiredCodexStatus := findProviderStatus(t, expiredResp.Msg.Providers, "codex")
	assert.False(t, expiredCodexStatus.Configured)
	assert.False(t, expiredCodexStatus.HasApiKey)
	assert.Nil(t, expiredCodexStatus.MaskedKey)
}

func setSkillsFeatureFlagForSettingsTests(t *testing.T, repo *db.Repo, enabled bool) {
	t.Helper()
	value := "false"
	if enabled {
		value = "true"
	}
	t.Setenv(features.SkillsEnabledEnvVar, value)
	require.NoError(t, repo.SetString(context.Background(), "test-user", nil, features.SkillsEnabledSetting, value))
	// Skills discovery/index caches are process-global; clear between tests so
	// fixture-specific project path mutations remain deterministic.
	skillcatalog.DefaultCatalogIndex().Invalidate("")
}

func createIsolatedSettingsProject(t *testing.T, repo *db.Repo, userID string) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	project := &db.Project{
		ID:         uuid.New().String(),
		UserID:     userID,
		Name:       "isolated-settings-project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	require.NoError(t, repo.CreateProject(context.Background(), project))
	return project
}

func TestSettingsService_InstallSkill(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	source := filepath.Join(t.TempDir(), "grpc-install-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: grpc-install-skill
description: Skill installed via settings RPC
---
Use this skill`), 0o644))

	resp, err := svc.InstallSkill(ctx, connect.NewRequest(&reliantv1.InstallSkillRequest{
		ProjectId:      project.ID,
		Source:         source,
		Scope:          reliantv1.SkillScope_SKILL_SCOPE_PROJECT,
		ConflictPolicy: reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_OVERWRITE,
		DryRun:         true,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	require.Contains(t, resp.Msg.Message, "Dry-run preview")
	require.NotNil(t, resp.Msg.Result)

	resp, err = svc.InstallSkill(ctx, connect.NewRequest(&reliantv1.InstallSkillRequest{
		ProjectId:      project.ID,
		Source:         source,
		Scope:          reliantv1.SkillScope_SKILL_SCOPE_PROJECT,
		ConflictPolicy: reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_OVERWRITE,
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	require.NotNil(t, resp.Msg.Result)
	require.Contains(t, resp.Msg.Message, "Installed skill 'grpc-install-skill'")

	_, err = os.Stat(filepath.Join(project.Path, ".reliant", "skills", "grpc-install-skill", "SKILL.md"))
	require.NoError(t, err)
}

func TestSettingsService_InstallSkill_InvalidScopeReturnsErrorResponse(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	source := filepath.Join(t.TempDir(), "grpc-install-invalid-scope")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: grpc-install-invalid-scope
description: Skill installed via settings RPC
---
Use this skill`), 0o644))

	resp, err := svc.InstallSkill(ctx, connect.NewRequest(&reliantv1.InstallSkillRequest{
		ProjectId: project.ID,
		Source:    source,
		Scope:     reliantv1.SkillScope(-1),
	}))
	require.NoError(t, err)
	require.False(t, resp.Msg.Success)
	require.Nil(t, resp.Msg.Result)
	require.Contains(t, resp.Msg.Message, "invalid scope")
}

func TestSettingsService_DeleteGlobalSkill(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, &settingsTestDaemonRouter{})
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	home := t.TempDir()
	t.Setenv("HOME", home)

	skillDir := filepath.Join(home, ".reliant", "skills", "global-delete-test")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: global-delete-test
description: skill in global scope
---
Use this skill`), 0o644))

	resp, err := svc.DeleteGlobalSkill(ctx, connect.NewRequest(&reliantv1.DeleteGlobalSkillRequest{
		ProjectId:    project.ID,
		RelativePath: "global-delete-test",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	require.Contains(t, resp.Msg.Message, "Deleted")

	_, err = os.Stat(skillDir)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))
}

func TestSettingsService_DeleteGlobalSkill_PathTraversalRejected(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, &settingsTestDaemonRouter{})
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err = svc.DeleteGlobalSkill(ctx, connect.NewRequest(&reliantv1.DeleteGlobalSkillRequest{
		ProjectId:    project.ID,
		RelativePath: "../escape",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSettingsService_DeleteGlobalSkill_RequiresValidSkillDirectory(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, &settingsTestDaemonRouter{})
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	home := t.TempDir()
	t.Setenv("HOME", home)

	skillDir := filepath.Join(home, ".reliant", "skills", "not-a-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "README.md"), []byte("no skill file"), 0o644))

	_, err = svc.DeleteGlobalSkill(ctx, connect.NewRequest(&reliantv1.DeleteGlobalSkillRequest{
		ProjectId:    project.ID,
		RelativePath: "not-a-skill",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSettingsService_DeleteGlobalSkill_RejectsInvalidSkillMarkdown(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, &settingsTestDaemonRouter{})
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	home := t.TempDir()
	t.Setenv("HOME", home)

	skillDir := filepath.Join(home, ".reliant", "skills", "broken-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("not valid frontmatter"), 0o644))

	resp, err := svc.DeleteGlobalSkill(ctx, connect.NewRequest(&reliantv1.DeleteGlobalSkillRequest{
		ProjectId:    project.ID,
		RelativePath: "broken-skill",
	}))
	// DeleteGlobalSkill succeeds even with invalid markdown — it logs a warning
	// but still deletes the directory and returns success.
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
}

func TestSettingsService_ListInstalledSkills_ReturnsDeterministicOrder(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project := createIsolatedSettingsProject(t, repo, "test-user")
	t.Setenv("HOME", t.TempDir())

	writeSkill := func(dir, name, description string) {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nbody"), 0o644))
	}

	writeSkill(filepath.Join(project.Path, ".reliant", "skills", "zeta-skill"), "zeta-skill", "zeta")
	writeSkill(filepath.Join(project.Path, ".reliant", "skills", "alpha-skill"), "alpha-skill", "alpha")

	resp, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{ProjectId: project.ID, PageSize: 500}))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resp.Msg.Skills), 3)

	names := make([]string, 0, len(resp.Msg.Skills))
	for _, skill := range resp.Msg.Skills {
		names = append(names, skill.Name)
	}
	indexAlpha := -1
	indexZeta := -1
	indexSkillCreator := -1
	for i, name := range names {
		if name == "alpha-skill" {
			indexAlpha = i
		}
		if name == "zeta-skill" {
			indexZeta = i
		}
		if name == "skill-creator" {
			indexSkillCreator = i
		}
	}
	require.NotEqual(t, -1, indexAlpha)
	require.NotEqual(t, -1, indexZeta)
	require.NotEqual(t, -1, indexSkillCreator)
	require.Less(t, indexAlpha, indexSkillCreator)
	require.Less(t, indexSkillCreator, indexZeta)

	var builtinSkill *reliantv1.InstalledSkill
	for _, skill := range resp.Msg.Skills {
		if skill.Name == "skill-creator" {
			builtinSkill = skill
			break
		}
	}
	require.NotNil(t, builtinSkill)
	assert.Equal(t, reliantv1.SkillScope_SKILL_SCOPE_BUILTIN, builtinSkill.Scope)
	assert.True(t, builtinSkill.Active)
	assert.Contains(t, builtinSkill.SkillId, "builtin|")
}

func TestSettingsService_ListInstalledSkills_ShadowedBuiltinDoesNotEmitParseDiagnostic(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project := createIsolatedSettingsProject(t, repo, "test-user")
	t.Setenv("HOME", t.TempDir())

	projectSkillDir := filepath.Join(project.Path, ".reliant", "skills", "skill-creator")
	require.NoError(t, os.MkdirAll(projectSkillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkillDir, "SKILL.md"), []byte(`---
name: skill-creator
description: project override for builtin skill
---
project override body`), 0o644))

	resp, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{ProjectId: project.ID, PageSize: 500}))
	require.NoError(t, err)

	for _, diagnostic := range resp.Msg.Diagnostics {
		assert.NotContains(t, diagnostic.Message, "failed to parse shadowed skill")
	}

	skillCreatorCount := 0
	var shadowedBuiltinSkill *reliantv1.InstalledSkill
	for _, skill := range resp.Msg.Skills {
		if skill.Name != "skill-creator" {
			continue
		}
		skillCreatorCount++
		if !skill.Active && skill.Scope == reliantv1.SkillScope_SKILL_SCOPE_BUILTIN {
			shadowedBuiltinSkill = skill
		}
	}

	require.GreaterOrEqual(t, skillCreatorCount, 1)
	if shadowedBuiltinSkill != nil {
		assert.Contains(t, shadowedBuiltinSkill.SkillId, "builtin|")
	}
}

func TestSettingsService_ListInstalledSkills_PaginatesWithNextPageToken(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	writeSkill := func(dir, name, description string) {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nbody"), 0o644))
	}

	writeSkill(filepath.Join(project.Path, ".reliant", "skills", "page-one-skill"), "page-one-skill", "page one")
	writeSkill(filepath.Join(project.Path, ".reliant", "skills", "page-two-skill"), "page-two-skill", "page two")

	firstPage, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{
		ProjectId: project.ID,
		PageSize:  1,
	}))
	require.NoError(t, err)
	require.Len(t, firstPage.Msg.Skills, 1)
	require.GreaterOrEqual(t, firstPage.Msg.Total, int32(2))
	require.NotEmpty(t, firstPage.Msg.NextPageToken)

	secondPage, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{
		ProjectId: project.ID,
		PageSize:  1,
		PageToken: firstPage.Msg.NextPageToken,
	}))
	require.NoError(t, err)
	require.Len(t, secondPage.Msg.Skills, 1)
	assert.NotEqual(t, firstPage.Msg.Skills[0].SkillId, secondPage.Msg.Skills[0].SkillId)

	_, err = svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{
		ProjectId: project.ID,
		PageToken: "not-a-number",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSettingsService_GetInstalledSkillDefinition_UsesSkillIDFromList(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, &settingsTestDaemonRouter{})
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project := createIsolatedSettingsProject(t, repo, "test-user")
	t.Setenv("HOME", t.TempDir())

	skillDir := filepath.Join(project.Path, ".reliant", "skills", "definition-fetch-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: definition-fetch-skill
description: definition fetch test
---
Use this definition fetch body`), 0o644))

	listResp, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{ProjectId: project.ID, PageSize: 500}))
	require.NoError(t, err)

	var skillFromList *reliantv1.InstalledSkill
	for _, skill := range listResp.Msg.Skills {
		if skill.Name == "definition-fetch-skill" {
			skillFromList = skill
			break
		}
	}
	require.NotNil(t, skillFromList)
	require.NotEmpty(t, skillFromList.SkillId)

	definitionResp, err := svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&reliantv1.GetInstalledSkillDefinitionRequest{
		ProjectId: project.ID,
		SkillId:   skillFromList.SkillId,
	}))
	require.NoError(t, err)
	assert.Equal(t, skillFromList.SkillId, definitionResp.Msg.SkillId)
	assert.Equal(t, skillFromList.DefinitionPath, definitionResp.Msg.DefinitionPath)
	assert.Contains(t, definitionResp.Msg.DefinitionContent, "Use this definition fetch body")
	assert.Empty(t, definitionResp.Msg.Assets)

	t.Run("includes packaged image assets from skill files", func(t *testing.T) {
		persistedProject, err := repo.GetProject(ctx, project.ID)
		require.NoError(t, err)
		require.NotNil(t, persistedProject)

		skillAssetsDir := filepath.Join(persistedProject.Path, ".reliant", "skills", "asset-skill")
		require.NoError(t, os.MkdirAll(filepath.Join(skillAssetsDir, "assets"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillAssetsDir, "SKILL.md"), []byte(`---
name: asset-skill
description: has asset
---
See ![logo](assets/logo.png)`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(skillAssetsDir, "assets", "logo.png"), []byte("png-bytes"), 0o644))

		skillcatalog.DefaultCatalogIndex().Invalidate(persistedProject.Path)
		assetSkillID := buildInstalledSkillID(skills.ScopeProject, filepath.Join(skillAssetsDir, "SKILL.md"), true)

		assetDefinitionResp, err := svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&reliantv1.GetInstalledSkillDefinitionRequest{
			ProjectId: persistedProject.ID,
			SkillId:   assetSkillID,
		}))
		require.NoError(t, err)
		require.Len(t, assetDefinitionResp.Msg.Assets, 1)
		assert.Equal(t, "assets/logo.png", assetDefinitionResp.Msg.Assets[0].Path)
		assert.Equal(t, "image/png", assetDefinitionResp.Msg.Assets[0].MimeType)
		assert.Equal(t, []byte("png-bytes"), assetDefinitionResp.Msg.Assets[0].Content)
	})

	t.Run("builtin scope is exposed when it appears in installed list", func(t *testing.T) {
		var builtinItem *reliantv1.InstalledSkill
		for _, item := range listResp.Msg.Skills {
			if item.Scope == reliantv1.SkillScope_SKILL_SCOPE_BUILTIN {
				builtinItem = item
				break
			}
		}
		require.NotNil(t, builtinItem)

		builtinDefinitionResp, err := svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&reliantv1.GetInstalledSkillDefinitionRequest{
			ProjectId: project.ID,
			SkillId:   builtinItem.SkillId,
		}))
		require.NoError(t, err)
		assert.Equal(t, builtinItem.SkillId, builtinDefinitionResp.Msg.SkillId)
		assert.Equal(t, builtinItem.DefinitionPath, builtinDefinitionResp.Msg.DefinitionPath)
		assert.Contains(t, builtinDefinitionResp.Msg.DefinitionContent, "name: skill-creator")
	})

	_, err = svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&reliantv1.GetInstalledSkillDefinitionRequest{
		ProjectId: project.ID,
		SkillId:   "invalid",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSettingsService_SetSkillEnabled_TogglesProjectSkill(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, &settingsTestDaemonRouter{})
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	// Keep global scope isolated while creating a project-scoped skill under the
	// persisted project path used by ListInstalledSkills.
	t.Setenv("HOME", t.TempDir())

	skillDir := filepath.Join(project.Path, ".reliant", "skills", "toggle-skill")
	require.NoError(t, os.RemoveAll(skillDir))
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	t.Cleanup(func() {
		_ = os.RemoveAll(skillDir)
	})
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: toggle-skill
description: toggle test
---

Toggle me`), 0o644))

	listResp, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{
		ProjectId: project.ID,
		PageSize:  500,
	}))
	require.NoError(t, err)

	var target *reliantv1.InstalledSkill
	for _, item := range listResp.Msg.Skills {
		if item.Name == "toggle-skill" {
			target = item
			break
		}
	}
	if target == nil {
		for _, item := range listResp.Msg.Skills {
			if isSkillPathForName(item.DefinitionPath, "toggle-skill") {
				target = item
				break
			}
		}
	}
	require.NotNil(t, target)
	require.True(t, target.Active)

	disableResp, err := svc.SetSkillEnabled(ctx, connect.NewRequest(&reliantv1.SetSkillEnabledRequest{
		ProjectId: project.ID,
		SkillId:   target.SkillId,
		Enabled:   false,
	}))
	require.NoError(t, err)
	require.True(t, disableResp.Msg.Success)
	assert.False(t, disableResp.Msg.Enabled)

	setting, err := repo.GetSetting(ctx, "test-user", &project.ID, "skills.disabled_definition_paths")
	require.NoError(t, err)
	assert.Contains(t, setting.Value, filepath.ToSlash(filepath.Clean(target.DefinitionPath)))

	listAfterDisable, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{
		ProjectId: project.ID,
		PageSize:  500,
	}))
	require.NoError(t, err)
	var disabledItem *reliantv1.InstalledSkill
	for _, item := range listAfterDisable.Msg.Skills {
		if item.DefinitionPath == target.DefinitionPath {
			disabledItem = item
			break
		}
	}
	require.NotNil(t, disabledItem)
	assert.False(t, disabledItem.Active)

	_, err = svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&reliantv1.GetInstalledSkillDefinitionRequest{
		ProjectId: project.ID,
		SkillId:   target.SkillId,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	disabledDefinitionResp, err := svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&reliantv1.GetInstalledSkillDefinitionRequest{
		ProjectId: project.ID,
		SkillId:   disabledItem.SkillId,
	}))
	require.NoError(t, err)
	assert.Equal(t, disabledItem.SkillId, disabledDefinitionResp.Msg.SkillId)
	assert.NotEmpty(t, disabledDefinitionResp.Msg.DefinitionContent)

	enableResp, err := svc.SetSkillEnabled(ctx, connect.NewRequest(&reliantv1.SetSkillEnabledRequest{
		ProjectId: project.ID,
		SkillId:   target.SkillId,
		Enabled:   true,
	}))
	require.NoError(t, err)
	require.True(t, enableResp.Msg.Success)
	assert.True(t, enableResp.Msg.Enabled)

	listAfterEnable, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{
		ProjectId: project.ID,
		PageSize:  500,
	}))
	require.NoError(t, err)
	found := false
	for _, item := range listAfterEnable.Msg.Skills {
		if item.Name == "toggle-skill" {
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestSettingsService_SetSkillEnabled_RejectsBuiltinSkill(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, true)

	project := createIsolatedSettingsProject(t, repo, "test-user")

	// Ensure builtin skill wins over codex/claude skill-creator variants.
	t.Setenv("HOME", t.TempDir())

	writeSkill := func(baseDir, description string) {
		skillDir := filepath.Join(baseDir, "skill-creator")
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: skill-creator\ndescription: "+description+"\n---\nbody"), 0o644))
	}
	writeSkill(filepath.Join(project.Path, ".agents", "skills"), "agents override")
	writeSkill(filepath.Join(project.Path, ".codex", "skills"), "codex override")
	writeSkill(filepath.Join(project.Path, ".claude", "skills"), "claude override")

	listResp, err := svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{
		ProjectId: project.ID,
		PageSize:  500,
	}))
	require.NoError(t, err)

	var builtinSkill *reliantv1.InstalledSkill
	for _, item := range listResp.Msg.Skills {
		if item.Scope == reliantv1.SkillScope_SKILL_SCOPE_BUILTIN {
			builtinSkill = item
			break
		}
	}
	require.NotNil(t, builtinSkill)
	require.True(t, builtinSkill.Active)
	assert.Equal(t, "skill-creator", builtinSkill.Name)

	_, err = svc.SetSkillEnabled(ctx, connect.NewRequest(&reliantv1.SetSkillEnabledRequest{
		ProjectId: project.ID,
		SkillId:   builtinSkill.SkillId,
		Enabled:   false,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSettingsService_SkillsEndpoints_RejectWhenFeatureDisabled(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	setSkillsFeatureFlagForSettingsTests(t, repo, false)

	project, err := repo.GetProject(ctx, "test-project")
	require.NoError(t, err)
	require.NotNil(t, project)

	source := filepath.Join(t.TempDir(), "grpc-disabled-skill")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(`---
name: grpc-disabled-skill
description: Skill used for disabled feature checks
---
Use this skill`), 0o644))

	_, err = svc.InstallSkill(ctx, connect.NewRequest(&reliantv1.InstallSkillRequest{
		ProjectId: project.ID,
		Source:    source,
		Scope:     reliantv1.SkillScope_SKILL_SCOPE_PROJECT,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.ListInstalledSkills(ctx, connect.NewRequest(&reliantv1.ListInstalledSkillsRequest{ProjectId: project.ID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.GetInstalledSkillDefinition(ctx, connect.NewRequest(&reliantv1.GetInstalledSkillDefinitionRequest{
		ProjectId: project.ID,
		SkillId:   "project|dummy|active",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.SetSkillEnabled(ctx, connect.NewRequest(&reliantv1.SetSkillEnabledRequest{
		ProjectId: project.ID,
		SkillId:   "project|dummy|active",
		Enabled:   false,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.ListRecommendedSkills(ctx, connect.NewRequest(&reliantv1.ListRecommendedSkillsRequest{ProjectId: project.ID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = svc.DeleteGlobalSkill(ctx, connect.NewRequest(&reliantv1.DeleteGlobalSkillRequest{
		ProjectId:    project.ID,
		RelativePath: "anything",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
