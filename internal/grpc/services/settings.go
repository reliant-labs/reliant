// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/features"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/skills"
	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/validation"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// SettingsService implements the SettingsService RPC handlers
type SettingsService struct {
	reliantv1connect.UnimplementedSettingsServiceHandler
	database           db.Repository
	daemonRouter       toolexec.DaemonRouter
	controlPlaneClient *controlplane.Client
}

// NewSettingsService creates a new SettingsService
func NewSettingsService(database db.Repository, daemonRouter toolexec.DaemonRouter, controlPlaneClient ...*controlplane.Client) *SettingsService {
	var cpClient *controlplane.Client
	if len(controlPlaneClient) > 0 {
		cpClient = controlPlaneClient[0]
	}
	return &SettingsService{
		database:           database,
		daemonRouter:       daemonRouter,
		controlPlaneClient: cpClient,
	}
}

// sendDaemonCommand sends a command to the user's daemon and unmarshals the response.
func (s *SettingsService) sendDaemonCommand(ctx context.Context, userID, commandType string, payload interface{}, resp interface{}) error {
	if s.daemonRouter == nil {
		return fmt.Errorf("daemon router not available")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	respBytes, err := s.daemonRouter.SendDaemonCommand(ctx, userID, commandType, payloadBytes, 30_000)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

// ============================================================================
// Helper Methods
// ============================================================================

// settingToProto converts a db.Setting to proto Setting
func settingToProto(s *db.Setting) *reliantv1.Setting {
	setting := &reliantv1.Setting{
		Id:        s.ID,
		Key:       s.Key,
		Value:     s.Value,
		ValueType: s.ValueType,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}
	if s.ProjectID != nil {
		setting.ProjectId = s.ProjectID
	}
	return setting
}

func skillsFeatureDisabledError() error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("skills feature is disabled"))
}

func reliantManagedAccessDisabledError() error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reliant managed access feature is disabled"))
}

func (s *SettingsService) ensureSkillsFeatureEnabled(ctx context.Context, userID string) error {
	if features.IsSkillsEnabledForContext(ctx, s.database, userID) {
		return nil
	}
	return skillsFeatureDisabledError()
}

func (s *SettingsService) ensureReliantManagedAccessEnabled(ctx context.Context, userID string) error {
	if features.IsReliantManagedAccessEnabledForContext(ctx, s.database, userID) {
		return nil
	}
	return reliantManagedAccessDisabledError()
}

// upsertSetting creates or updates a setting
func (s *SettingsService) upsertSetting(ctx context.Context, userID string, projectID *string, key, value string) error {
	setting, err := s.database.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		if !isSettingNotFoundError(err) {
			return err
		}

		// Setting doesn't exist, create it
		newSetting := &db.Setting{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: projectID,
			Key:       key,
			Value:     value,
			ValueType: "string",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		return s.database.CreateSetting(ctx, newSetting)
	}

	// Setting exists, update it
	setting.Value = value
	setting.UpdatedAt = time.Now().UTC()
	return s.database.UpdateSetting(ctx, setting)
}

func isSettingNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "setting not found")
}

// validateAPIKey validates an API key for a provider
func (s *SettingsService) validateAPIKey(ctx context.Context, provider models.Family, apiKey string) (bool, string) {
	registry := models.MustGetRegistry()
	providerDefs := registry.ListModelsByProvider(string(provider))
	if len(providerDefs) == 0 {
		return false, fmt.Sprintf("No models found for provider: %s", provider)
	}
	testModel := providerDefs[0].ToModel()

	driver, err := drivers.GetDriverForModel(testModel, provider,
		llm.WithAPIKey(apiKey),
		llm.WithMaxTokens(100),
	)
	if err != nil {
		logging.Error("Failed to create driver for validation", "provider", provider, "error", err)
		return false, fmt.Sprintf("Failed to initialize provider: %v", err)
	}

	err = driver.ValidateKey(ctx)
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "unauthorized") ||
			strings.Contains(errStr, "authentication") ||
			strings.Contains(errStr, "invalid") ||
			strings.Contains(errStr, "api key") ||
			strings.Contains(errStr, "401") {
			return false, "Invalid API key"
		}
		logging.Warn("API key validation error (may not be auth-related)", "provider", provider, "error", err)
		return false, fmt.Sprintf("Validation failed: %v", err)
	}

	return true, "API key is valid"
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func maskStoredCredential(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	masked := "***"
	if len(value) > 8 {
		masked = value[:4] + "..." + value[len(value)-4:]
	}
	return &masked
}

func (s *SettingsService) hasControlPlaneClient() bool {
	return s.controlPlaneClient != nil && s.controlPlaneClient.Enabled()
}

func (s *SettingsService) getReliantStoredToken(ctx context.Context, userID string) (string, error) {
	token, err := s.database.GetProviderAPIKey(ctx, userID, "reliant")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(token), nil
}

func requireUserJWT(ctx context.Context) (string, error) {
	jwt, ok := auth.GetUserJWTFromContext(ctx)
	jwt = strings.TrimSpace(jwt)
	if !ok || jwt == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("signed-in session required"))
	}
	return jwt, nil
}

func controlPlaneErrorMessage(err error, fallback string) string {
	if errors.Is(err, controlplane.ErrNotConfigured) {
		return "Reliant control-plane client is not configured"
	}
	var rpcErr *controlplane.RPCError
	if errors.As(err, &rpcErr) {
		if message := strings.TrimSpace(rpcErr.Message); message != "" {
			return message
		}
	}
	if err != nil {
		if message := strings.TrimSpace(err.Error()); message != "" {
			return message
		}
	}
	return fallback
}

func controlPlaneErrorCode(err error) string {
	if errors.Is(err, controlplane.ErrNotConfigured) {
		return "failed_precondition"
	}
	var rpcErr *controlplane.RPCError
	if errors.As(err, &rpcErr) {
		return strings.ToLower(strings.TrimSpace(rpcErr.Code))
	}
	return ""
}

func controlPlaneConnectCode(err error) connect.Code {
	switch controlPlaneErrorCode(err) {
	case "unauthenticated":
		return connect.CodeUnauthenticated
	case "failed_precondition":
		return connect.CodeFailedPrecondition
	case "resource_exhausted":
		return connect.CodeResourceExhausted
	case "unavailable":
		return connect.CodeUnavailable
	case "internal":
		return connect.CodeInternal
	}

	var rpcErr *controlplane.RPCError
	if errors.As(err, &rpcErr) && rpcErr.StatusCode >= 500 {
		return connect.CodeUnavailable
	}
	if err != nil {
		return connect.CodeUnavailable
	}
	return connect.CodeInternal
}

func mapControlPlaneError(err error, fallback string) error {
	return connect.NewError(controlPlaneConnectCode(err), fmt.Errorf("%s", controlPlaneErrorMessage(err, fallback)))
}

func reliantProviderTokenToProto(token controlplane.DaemonToken) *reliantv1.ReliantProviderToken {
	return &reliantv1.ReliantProviderToken{
		Id:          token.ID,
		Name:        token.Name,
		TokenPrefix: token.TokenPrefix,
		Ephemeral:   token.Ephemeral,
		CreatedAt:   token.CreatedAt,
		LastUsedAt:  stringPtr(token.LastUsedAt),
		ExpiresAt:   stringPtr(token.ExpiresAt),
		RevokedAt:   stringPtr(token.RevokedAt),
	}
}

func reliantAccessStatusFromGrant(access *controlplane.GetCurrentLLMAccessResponse) *reliantv1.ReliantProviderAccessStatus {
	if access == nil {
		return nil
	}
	message := "Connected to Reliant"
	if planCode := strings.TrimSpace(access.PlanCode); planCode != "" {
		message = fmt.Sprintf("Connected to Reliant (%s)", planCode)
	}
	return &reliantv1.ReliantProviderAccessStatus{
		State:               "connected",
		Message:             message,
		PlanId:              stringPtr(access.PlanID),
		PlanCode:            stringPtr(access.PlanCode),
		AllowedModels:       append([]string(nil), access.AllowedModels...),
		RequestTags:         append([]string(nil), access.RequestTags...),
		Spend:               access.Spend,
		HardBudgetUsd:       access.HardBudgetUSD,
		BudgetDuration:      access.BudgetDuration,
		RpmLimit:            access.RPMLimit,
		TpmLimit:            access.TPMLimit,
		MaxParallelRequests: access.MaxParallelRequests,
		KeyDuration:         access.KeyDuration,
	}
}

func reliantAccessStatusFromError(err error, fallback string) *reliantv1.ReliantProviderAccessStatus {
	if err == nil {
		return nil
	}
	state := controlPlaneErrorCode(err)
	if state == "" {
		switch controlPlaneConnectCode(err) {
		case connect.CodeUnauthenticated:
			state = "unauthenticated"
		case connect.CodeFailedPrecondition:
			state = "failed_precondition"
		case connect.CodeResourceExhausted:
			state = "resource_exhausted"
		case connect.CodeUnavailable:
			state = "unavailable"
		default:
			state = "internal"
		}
	}
	return &reliantv1.ReliantProviderAccessStatus{
		State:   state,
		Message: controlPlaneErrorMessage(err, fallback),
	}
}

func isReliantSkillScope(scope skills.Scope) bool {
	switch scope {
	case skills.ScopeProjectLocal, skills.ScopeProject, skills.ScopeGlobal, skills.ScopeBuiltin:
		return true
	default:
		return false
	}
}

func normalizeGlobalSkillRelativePath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", fmt.Errorf("relative_path is required")
	}

	path = strings.ReplaceAll(path, `\\`, "/")
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, ".reliant/skills/")

	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("relative_path is required")
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("relative_path must be under ~/.reliant/skills")
	}

	return clean, nil
}

func parseSkillFromContent(path string, scope skills.Scope, blob []byte) (skills.Skill, error) {
	base := strings.ToLower(filepath.Base(path))
	if base != "skill.md" {
		return skills.Skill{}, fmt.Errorf("unsupported skill definition file: %s", path)
	}
	definition, err := skillcatalog.ParseSkillMarkdown(path, scope, blob)
	if err != nil {
		return skills.Skill{}, err
	}
	return catalogDefinitionToSkill(definition), nil
}

// readSkillFile reads a file via the daemon router. Returns content and whether the file was found.
func (s *SettingsService) readSkillFile(ctx context.Context, userID, path string) ([]byte, bool, error) {
	var resp struct {
		Content string `json:"content"`
		Found   bool   `json:"found"`
	}
	if err := s.sendDaemonCommand(ctx, userID, "skills.read_file", map[string]string{"path": path}, &resp); err != nil {
		return nil, false, err
	}
	if !resp.Found {
		return nil, false, nil
	}
	return []byte(resp.Content), true, nil
}

func catalogDefinitionToSkill(definition skillcatalog.Definition) skills.Skill {
	skill := skills.Skill{
		Name:          definition.Name,
		NormalizedKey: definition.NormalizedKey,
		Description:   definition.Description,
		License:       definition.License,
		Compatibility: definition.Compatibility,
		Body:          definition.Body,
		Path:          definition.Path,
		Scope:         definition.Scope,
		Format:        definition.Format,
		SkillDir:      definition.SkillDir,
	}
	if definition.Metadata != nil {
		skill.Metadata = make(map[string]string, len(definition.Metadata))
		for key, value := range definition.Metadata {
			skill.Metadata[key] = value
		}
	}
	if definition.AllowedTools != nil {
		skill.AllowedTools = append([]string(nil), definition.AllowedTools...)
	}
	return skill
}

func (s *SettingsService) collectPackagedSkillImageAssets(ctx context.Context, userID string, skill skills.Skill) []*reliantv1.SkillAsset {
	if skill.Scope == skills.ScopeBuiltin {
		return nil
	}

	skillDir := strings.TrimSpace(skill.SkillDir)
	if skillDir == "" {
		return nil
	}

	type assetEntry struct {
		RelativePath string `json:"relative_path"`
		MimeType     string `json:"mime_type"`
		ContentB64   string `json:"content_b64"`
	}
	var resp struct {
		Assets []assetEntry `json:"assets"`
	}
	if err := s.sendDaemonCommand(ctx, userID, "skills.read_skill_assets", map[string]string{"skill_dir": skillDir}, &resp); err != nil {
		logging.Warn("Failed to read skill assets via daemon", "skillPath", skill.Path, "error", err)
		return nil
	}

	assets := make([]*reliantv1.SkillAsset, 0, len(resp.Assets))
	for _, entry := range resp.Assets {
		blob, err := base64.StdEncoding.DecodeString(entry.ContentB64)
		if err != nil {
			logging.Warn("Failed to decode skill asset", "skillPath", skill.Path, "assetPath", entry.RelativePath, "error", err)
			continue
		}
		assets = append(assets, &reliantv1.SkillAsset{
			Path:     entry.RelativePath,
			MimeType: entry.MimeType,
			Content:  blob,
		})
	}

	sort.Slice(assets, func(i, j int) bool {
		return assets[i].Path < assets[j].Path
	})

	return assets
}

func parseDisabledSkillDefinitionPaths(raw string) map[string]struct{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var paths []string
	if err := json.Unmarshal([]byte(trimmed), &paths); err != nil {
		return nil
	}
	if len(paths) == 0 {
		return nil
	}

	out := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		canonical := skills.CanonicalDefinitionPath(path)
		if canonical != "" {
			out[canonical] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func encodeDisabledSkillDefinitionPaths(paths map[string]struct{}) string {
	if len(paths) == 0 {
		return "[]"
	}

	ordered := make([]string, 0, len(paths))
	for path := range paths {
		canonical := skills.CanonicalDefinitionPath(path)
		if canonical != "" {
			ordered = append(ordered, canonical)
		}
	}
	sort.Strings(ordered)

	blob, err := json.Marshal(ordered)
	if err != nil {
		return "[]"
	}
	return string(blob)
}

func loadDisabledSkillDefinitionPathSet(ctx context.Context, repo db.Repository, userID string, projectID string) (map[string]struct{}, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project_id is required")
	}

	setting, err := repo.GetSetting(ctx, userID, &projectID, skills.DisabledDefinitionPathsSettingKey)
	if err != nil {
		if isSettingNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}

	return parseDisabledSkillDefinitionPaths(setting.Value), nil
}

func inferReliantSkillScope(projectPath, skillPath, homeDir string) (skills.Scope, bool) {
	normalized := filepath.Clean(skillPath)
	projectLocalRoot := filepath.Clean(filepath.Join(projectPath, ".reliant.local", "skills"))
	projectRoot := filepath.Clean(filepath.Join(projectPath, ".reliant", "skills"))

	if normalized == projectLocalRoot || strings.HasPrefix(normalized, projectLocalRoot+string(filepath.Separator)) {
		return skills.ScopeProjectLocal, true
	}
	if normalized == projectRoot || strings.HasPrefix(normalized, projectRoot+string(filepath.Separator)) {
		return skills.ScopeProject, true
	}

	if homeDir != "" {
		globalRoot := filepath.Clean(filepath.Join(homeDir, ".reliant", "skills"))
		if normalized == globalRoot || strings.HasPrefix(normalized, globalRoot+string(filepath.Separator)) {
			return skills.ScopeGlobal, true
		}
	}

	builtinPath := canonicalBuiltinDefinitionPath(normalized)
	if blob, err := skillcatalog.ReadBuiltinSkillDefinition(builtinPath); err == nil && len(blob) > 0 {
		return skills.ScopeBuiltin, true
	}

	return "", false
}

func skillScopeFromProto(scope reliantv1.SkillScope) string {
	switch scope {
	case reliantv1.SkillScope_SKILL_SCOPE_PROJECT_LOCAL:
		return tools.SkillInstallScopeProjectLocal
	case reliantv1.SkillScope_SKILL_SCOPE_PROJECT:
		return tools.SkillInstallScopeProject
	case reliantv1.SkillScope_SKILL_SCOPE_GLOBAL:
		return tools.SkillInstallScopeGlobal
	case reliantv1.SkillScope_SKILL_SCOPE_UNSPECIFIED:
		return ""
	default:
		return scope.String()
	}
}

func skillScopeToProto(scope string) reliantv1.SkillScope {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case tools.SkillInstallScopeProjectLocal:
		return reliantv1.SkillScope_SKILL_SCOPE_PROJECT_LOCAL
	case tools.SkillInstallScopeProject:
		return reliantv1.SkillScope_SKILL_SCOPE_PROJECT
	case tools.SkillInstallScopeGlobal:
		return reliantv1.SkillScope_SKILL_SCOPE_GLOBAL
	case string(skills.ScopeBuiltin):
		return reliantv1.SkillScope_SKILL_SCOPE_BUILTIN
	default:
		return reliantv1.SkillScope_SKILL_SCOPE_UNSPECIFIED
	}
}

func skillConflictPolicyFromProto(policy reliantv1.SkillConflictPolicy) string {
	switch policy {
	case reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_SKIP:
		return tools.SkillInstallConflictSkip
	case reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_OVERWRITE:
		return tools.SkillInstallConflictOverwrite
	case reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_RENAME:
		return tools.SkillInstallConflictRename
	case reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_UNSPECIFIED:
		return ""
	default:
		return policy.String()
	}
}

func skillConflictPolicyToProto(policy string) reliantv1.SkillConflictPolicy {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case tools.SkillInstallConflictSkip:
		return reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_SKIP
	case tools.SkillInstallConflictOverwrite:
		return reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_OVERWRITE
	case tools.SkillInstallConflictRename:
		return reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_RENAME
	default:
		return reliantv1.SkillConflictPolicy_SKILL_CONFLICT_POLICY_UNSPECIFIED
	}
}

func skillSourceTypeToProto(sourceType string) reliantv1.SkillSourceType {
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "local":
		return reliantv1.SkillSourceType_SKILL_SOURCE_TYPE_LOCAL
	case "git":
		return reliantv1.SkillSourceType_SKILL_SOURCE_TYPE_GIT
	default:
		return reliantv1.SkillSourceType_SKILL_SOURCE_TYPE_UNSPECIFIED
	}
}

func skillFormatToProto(format skills.SkillFormat) reliantv1.SkillFormat {
	switch format {
	case skills.SkillFormatClaudeMarkdown:
		return reliantv1.SkillFormat_SKILL_FORMAT_CLAUDE_MARKDOWN
	default:
		return reliantv1.SkillFormat_SKILL_FORMAT_UNSPECIFIED
	}
}

func parsePageSize(value int32, fallback, max int) int {
	if value <= 0 {
		return fallback
	}
	if int(value) > max {
		return max
	}
	return int(value)
}

func parsePageOffset(token string, total int) (int, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid page_token")
	}
	if offset < 0 || offset > total {
		return 0, fmt.Errorf("invalid page_token")
	}
	return offset, nil
}

func buildNextPageToken(nextOffset, total int) string {
	if nextOffset >= total {
		return ""
	}
	return strconv.Itoa(nextOffset)
}

func buildInstalledSkillID(scope skills.Scope, definitionPath string, active bool) string {
	status := "shadowed"
	if active {
		status = "active"
	}
	return fmt.Sprintf("%s|%s|%s", scope, filepath.Clean(definitionPath), status)
}

func canonicalBuiltinDefinitionPath(raw string) string {
	normalized := filepath.ToSlash(filepath.Clean(raw))
	if normalized == "" || normalized == "." {
		return normalized
	}
	if strings.HasPrefix(normalized, "builtin/") {
		return normalized
	}
	if filepath.IsAbs(filepath.FromSlash(normalized)) || strings.HasPrefix(normalized, "../") || normalized == ".." {
		return normalized
	}
	return filepath.ToSlash(filepath.Join("builtin", normalized))
}

func (s *SettingsService) resolveInstalledSkillByID(ctx context.Context, userID, projectPath, skillID string, disabledDefinitionPathSet map[string]struct{}) (skills.Skill, bool, error) {
	parts := strings.Split(skillID, "|")
	if len(parts) != 3 {
		return skills.Skill{}, false, fmt.Errorf("invalid skill_id")
	}
	scopeRaw := strings.TrimSpace(parts[0])
	definitionPath := strings.TrimSpace(parts[1])
	status := strings.TrimSpace(parts[2])
	if scopeRaw == "" || definitionPath == "" || (status != "active" && status != "shadowed") {
		return skills.Skill{}, false, fmt.Errorf("invalid skill_id")
	}

	if scopeRaw == string(skills.ScopeBuiltin) {
		canonicalDefinitionPath := canonicalBuiltinDefinitionPath(definitionPath)
		discovered := skills.DefaultRuntime().Discover(ctx, skills.DiscoverInput{
			ProjectPath:               projectPath,
			DisabledDefinitionPathSet: disabledDefinitionPathSet,
			LoadFullDefinitions:       false,
		})
		for _, skill := range discovered.Skills {
			if skill.Scope != skills.ScopeBuiltin {
				continue
			}
			candidatePath := filepath.ToSlash(filepath.Join(skill.SkillDir, "SKILL.md"))
			if candidatePath != canonicalDefinitionPath {
				continue
			}

			// Builtin skills are embedded and do not exist on disk. Keep the canonical
			// discovery path for identity, but swap in embedded content path for reads.
			resolved := skill
			resolved.Path = candidatePath
			return resolved, status == "active", nil
		}
		return skills.Skill{}, false, connect.NewError(connect.CodeNotFound, fmt.Errorf("skill not found"))
	}

	homeDir := s.getDaemonHomeDir(ctx, userID)
	resolvedScope, ok := inferReliantSkillScope(projectPath, definitionPath, homeDir)
	if !ok {
		return skills.Skill{}, false, fmt.Errorf("skill path is outside managed scopes")
	}
	if string(resolvedScope) != scopeRaw {
		return skills.Skill{}, false, fmt.Errorf("skill scope mismatch")
	}
	if len(disabledDefinitionPathSet) > 0 {
		if _, disabled := disabledDefinitionPathSet[skills.CanonicalDefinitionPath(definitionPath)]; disabled {
			// Allow resolving non-active IDs so callers can still inspect disabled skills
			// (e.g. UI details/icon hydration) while keeping active IDs non-resolvable.
			if status == "active" {
				return skills.Skill{}, false, connect.NewError(connect.CodeNotFound, fmt.Errorf("skill not found"))
			}
		}
	}

	blob, found, readErr := s.readSkillFile(ctx, userID, definitionPath)
	if readErr != nil {
		return skills.Skill{}, false, readErr
	}
	if !found {
		return skills.Skill{}, false, connect.NewError(connect.CodeNotFound, fmt.Errorf("skill definition not found: %s", definitionPath))
	}

	skill, err := parseSkillFromContent(definitionPath, resolvedScope, blob)
	if err != nil {
		return skills.Skill{}, false, err
	}
	return skill, status == "active", nil
}

// getDaemonHomeDir fetches the user's home directory from the daemon.
func (s *SettingsService) getDaemonHomeDir(ctx context.Context, userID string) string {
	var resp struct {
		HomeDir string `json:"home_dir"`
	}
	if err := s.sendDaemonCommand(ctx, userID, "skills.get_home_dir", struct{}{}, &resp); err != nil {
		return ""
	}
	return resp.HomeDir
}

// ============================================================================
// Sub-phase 6a: Core Settings CRUD
// ============================================================================

// CreateSetting creates a new setting
func (s *SettingsService) CreateSetting(ctx context.Context, req *connect.Request[reliantv1.CreateSettingRequest]) (*connect.Response[reliantv1.CreateSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}
	if req.Msg.Value == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("value is required"))
	}

	valueType := "string"
	if req.Msg.ValueType != nil {
		valueType = *req.Msg.ValueType
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	now := time.Now().UTC()
	setting := &db.Setting{
		ID:        uuid.New().String(),
		UserID:    userID,
		ProjectID: projectID,
		Key:       req.Msg.Key,
		Value:     req.Msg.Value,
		ValueType: valueType,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.database.CreateSetting(ctx, setting); err != nil {
		logging.Error("Failed to create setting", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create setting"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.Warn("Failed to emit config_health refetch after CreateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.CreateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// ListSettings lists all settings for the current user
func (s *SettingsService) ListSettings(ctx context.Context, req *connect.Request[reliantv1.ListSettingsRequest]) (*connect.Response[reliantv1.ListSettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	settings, err := s.database.ListSettings(ctx, userID, projectID)
	if err != nil {
		logging.Error("Failed to list settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list settings"))
	}

	protoSettings := make([]*reliantv1.Setting, len(settings))
	for i, setting := range settings {
		protoSettings[i] = settingToProto(setting)
	}

	return connect.NewResponse(&reliantv1.ListSettingsResponse{
		Settings: protoSettings,
		Total:    int32(len(protoSettings)),
	}), nil
}

// GetSetting retrieves a setting by key
func (s *SettingsService) GetSetting(ctx context.Context, req *connect.Request[reliantv1.GetSettingRequest]) (*connect.Response[reliantv1.GetSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	if req.Msg.Key == features.SkillsEnabledSetting {
		now := time.Now().UTC().Format(time.RFC3339)
		enabled := features.IsSkillsEnabledForContext(ctx, s.database, userID)
		return connect.NewResponse(&reliantv1.GetSettingResponse{
			Setting: &reliantv1.Setting{
				Key:       req.Msg.Key,
				Value:     strconv.FormatBool(enabled),
				ValueType: "bool",
				CreatedAt: now,
				UpdatedAt: now,
			},
		}), nil
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.database.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		// Return empty response instead of error for missing settings.
		// This prevents frontend callers from throwing when a setting
		// simply hasn't been created yet.
		return connect.NewResponse(&reliantv1.GetSettingResponse{
			Setting: nil,
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// UpdateSetting updates an existing setting
func (s *SettingsService) UpdateSetting(ctx context.Context, req *connect.Request[reliantv1.UpdateSettingRequest]) (*connect.Response[reliantv1.UpdateSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.database.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("setting not found"))
	}

	if req.Msg.Value != nil {
		setting.Value = *req.Msg.Value
	}
	if req.Msg.ValueType != nil {
		setting.ValueType = *req.Msg.ValueType
	}
	setting.UpdatedAt = time.Now().UTC()

	if err := s.database.UpdateSetting(ctx, setting); err != nil {
		logging.Error("Failed to update setting", "error", err, "key", req.Msg.Key)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update setting"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.Warn("Failed to emit config_health refetch after UpdateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// DeleteSetting deletes a setting by key
func (s *SettingsService) DeleteSetting(ctx context.Context, req *connect.Request[reliantv1.DeleteSettingRequest]) (*connect.Response[reliantv1.DeleteSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.database.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("setting not found"))
	}

	if err := s.database.DeleteSetting(ctx, setting.ID); err != nil {
		logging.Error("Failed to delete setting", "error", err, "key", req.Msg.Key)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete setting"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.Warn("Failed to emit config_health refetch after DeleteSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.DeleteSettingResponse{
		Success: true,
		Message: "Setting deleted successfully",
	}), nil
}

// ============================================================================
// Sub-phase 6b: UI Settings (Shortcuts/Preferences)
// ============================================================================

// GetShortcuts retrieves user keyboard shortcuts
func (s *SettingsService) GetShortcuts(ctx context.Context, req *connect.Request[reliantv1.GetShortcutsRequest]) (*connect.Response[reliantv1.GetShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.database.GetSetting(ctx, userID, nil, "shortcuts")
	if err != nil {
		// Return empty shortcuts if not found (frontend will use defaults)
		return connect.NewResponse(&reliantv1.GetShortcutsResponse{
			Shortcuts: "",
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetShortcutsResponse{
		Shortcuts: setting.Value,
	}), nil
}

// UpdateShortcuts updates user keyboard shortcuts
func (s *SettingsService) UpdateShortcuts(ctx context.Context, req *connect.Request[reliantv1.UpdateShortcutsRequest]) (*connect.Response[reliantv1.UpdateShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.upsertSetting(ctx, userID, nil, "shortcuts", req.Msg.Shortcuts); err != nil {
		logging.Error("Failed to update shortcuts", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update shortcuts"))
	}

	return connect.NewResponse(&reliantv1.UpdateShortcutsResponse{
		Success: true,
		Message: "Shortcuts updated successfully",
	}), nil
}

// GetPreferences retrieves user preferences
func (s *SettingsService) GetPreferences(ctx context.Context, req *connect.Request[reliantv1.GetPreferencesRequest]) (*connect.Response[reliantv1.GetPreferencesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get streaming enabled
	streamingEnabled := true
	if setting, err := s.database.GetSetting(ctx, userID, nil, "features.streaming_enabled"); err == nil {
		streamingEnabled = setting.Value == "true"
	}

	// Worktree archive mode
	worktreeArchiveMode := "ask_me"
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.archive_cleanup_mode"); err == nil {
		worktreeArchiveMode = setting.Value
	}

	// Worktree delete directory default
	worktreeDeleteDir := true
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.default_delete_directory"); err == nil {
		worktreeDeleteDir = setting.Value == "true"
	}

	// Worktree delete branch default
	worktreeDeleteBranch := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.default_delete_branch"); err == nil {
		worktreeDeleteBranch = setting.Value == "true"
	}

	// Branch copy uncommitted files default (defaults to false if not set)
	branchCopyUncommittedFilesDefault := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "worktree.branch_copy_uncommitted_files_default"); err == nil {
		branchCopyUncommittedFilesDefault = setting.Value == "true"
	}

	// Default MCP scope (defaults to PROJECT if not set)
	defaultMcpScope := reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_mcp_scope"); err == nil {
		if scope, ok := reliantv1.ConfigScope_value[setting.Value]; ok {
			defaultMcpScope = reliantv1.ConfigScope(scope)
		}
	}

	// Default workflow scope (defaults to PROJECT if not set)
	defaultWorkflowScope := reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_workflow_scope"); err == nil {
		if scope, ok := reliantv1.ConfigScope_value[setting.Value]; ok {
			defaultWorkflowScope = reliantv1.ConfigScope(scope)
		}
	}

	// Default workflow (defaults to builtin://agent if not set)
	defaultWorkflow := workflow.DefaultWorkflow
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_workflow"); err == nil && setting.Value != "" {
		defaultWorkflow = setting.Value
	}

	// Hide builtin workflows (defaults to false)
	hideBuiltinWorkflows := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "ui.hide_builtin_workflows"); err == nil {
		hideBuiltinWorkflows = setting.Value == "true"
	}

	// Hide builtin presets (defaults to false)
	hideBuiltinPresets := false
	if setting, err := s.database.GetSetting(ctx, userID, nil, "ui.hide_builtin_presets"); err == nil {
		hideBuiltinPresets = setting.Value == "true"
	}

	// Collect additional arbitrary preferences
	// Filter out keys that have dedicated fields to avoid duplicates
	excludedPrefKeys := map[string]bool{
		"default_planning_mode": true, // Has dedicated field
		"default_auto_approve":  true, // Has dedicated field
	}
	additional := make(map[string]string)
	allSettings, err := s.database.ListSettings(ctx, userID, nil)
	if err == nil {
		for _, setting := range allSettings {
			if len(setting.Key) > 11 && setting.Key[:11] == "preference." {
				key := setting.Key[11:] // Remove "preference." prefix
				if !excludedPrefKeys[key] {
					additional[key] = setting.Value
				}
			}
		}
	}

	// Compute effective hidden items by combining defaults and user overrides
	hiddenWorkflows, err := s.getEffectiveHiddenItems(ctx, userID, int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get hidden workflows: %w", err))
	}
	hiddenPresets, err := s.getEffectiveHiddenItems(ctx, userID, int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get hidden presets: %w", err))
	}

	return connect.NewResponse(&reliantv1.GetPreferencesResponse{
		StreamingEnabled:                  streamingEnabled,
		WorktreeArchiveMode:               worktreeArchiveMode,
		WorktreeDefaultDeleteDirectory:    worktreeDeleteDir,
		WorktreeDefaultDeleteBranch:       worktreeDeleteBranch,
		Additional:                        additional,
		BranchCopyUncommittedFilesDefault: branchCopyUncommittedFilesDefault,
		DefaultMcpScope:                   defaultMcpScope,
		DefaultWorkflowScope:              defaultWorkflowScope,
		DefaultWorkflow:                   defaultWorkflow,
		HideBuiltinWorkflows:              hideBuiltinWorkflows,
		HideBuiltinPresets:                hideBuiltinPresets,
		HiddenWorkflowSlugs:               hiddenWorkflows,
		HiddenPresetSlugs:                 hiddenPresets,
	}), nil
}

// UpdatePreferences updates user preferences
func (s *SettingsService) UpdatePreferences(ctx context.Context, req *connect.Request[reliantv1.UpdatePreferencesRequest]) (*connect.Response[reliantv1.UpdatePreferencesResponse], error) {
	userID := auth.MustGetUserID(ctx)
	analyticsClient := analytics.GetClientForUser(ctx, userID)
	changedKeys := make([]string, 0, 8)
	modelProviderSettingsChanged := false
	markChanged := func(key string) {
		changedKeys = append(changedKeys, key)
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "model") || strings.Contains(lowerKey, "provider") || strings.Contains(lowerKey, "tag") {
			modelProviderSettingsChanged = true
		}
	}

	if req.Msg.StreamingEnabled != nil {
		value := "false"
		if *req.Msg.StreamingEnabled {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "features.streaming_enabled", value); err != nil {
			logging.Error("Failed to update streaming_enabled", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("features.streaming_enabled")
	}

	if req.Msg.WorktreeArchiveMode != nil {
		mode := *req.Msg.WorktreeArchiveMode
		if mode != "ask_me" && mode != "always_cleanup" && mode != "always_keep" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid worktree_archive_mode. Must be 'ask_me', 'always_cleanup', or 'always_keep'"))
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.archive_cleanup_mode", mode); err != nil {
			logging.Error("Failed to update worktree.archive_cleanup_mode", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.archive_cleanup_mode")
	}

	if req.Msg.WorktreeDefaultDeleteDirectory != nil {
		value := "false"
		if *req.Msg.WorktreeDefaultDeleteDirectory {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.default_delete_directory", value); err != nil {
			logging.Error("Failed to update worktree.default_delete_directory", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.default_delete_directory")
	}

	if req.Msg.WorktreeDefaultDeleteBranch != nil {
		value := "false"
		if *req.Msg.WorktreeDefaultDeleteBranch {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.default_delete_branch", value); err != nil {
			logging.Error("Failed to update worktree.default_delete_branch", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.default_delete_branch")
	}

	// Handle default auto-approve for new chats
	if req.Msg.DefaultAutoApprove != nil {
		value := "false"
		if *req.Msg.DefaultAutoApprove {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "chat.default_auto_approve", value); err != nil {
			logging.Error("Failed to update chat.default_auto_approve", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("chat.default_auto_approve")
	}

	// Handle branch copy uncommitted files default
	if req.Msg.BranchCopyUncommittedFilesDefault != nil {
		value := "false"
		if *req.Msg.BranchCopyUncommittedFilesDefault {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "worktree.branch_copy_uncommitted_files_default", value); err != nil {
			logging.Error("Failed to update worktree.branch_copy_uncommitted_files_default", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("worktree.branch_copy_uncommitted_files_default")
	}

	// Handle additional arbitrary preferences
	for key, value := range req.Msg.Additional {
		prefKey := fmt.Sprintf("preference.%s", key)
		if err := s.upsertSetting(ctx, userID, nil, prefKey, value); err != nil {
			logging.Error("Failed to update preference", "key", key, "error", err)
			// Don't fail the whole request for one preference
			continue
		}
		markChanged(prefKey)
	}

	// Handle default MCP scope
	if req.Msg.DefaultMcpScope != nil {
		scopeValue := req.Msg.DefaultMcpScope.String()
		if err := s.upsertSetting(ctx, userID, nil, "config.default_mcp_scope", scopeValue); err != nil {
			logging.Error("Failed to update config.default_mcp_scope", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("config.default_mcp_scope")
	}

	// Handle default workflow scope
	if req.Msg.DefaultWorkflowScope != nil {
		scopeValue := req.Msg.DefaultWorkflowScope.String()
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow_scope", scopeValue); err != nil {
			logging.Error("Failed to update config.default_workflow_scope", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("config.default_workflow_scope")
	}

	// Handle default workflow
	if req.Msg.DefaultWorkflow != nil {
		if err := s.upsertSetting(ctx, userID, nil, "config.default_workflow", *req.Msg.DefaultWorkflow); err != nil {
			logging.Error("Failed to update config.default_workflow", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("config.default_workflow")
	}

	// Handle hide builtin workflows
	if req.Msg.HideBuiltinWorkflows != nil {
		value := "false"
		if *req.Msg.HideBuiltinWorkflows {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "ui.hide_builtin_workflows", value); err != nil {
			logging.Error("Failed to update ui.hide_builtin_workflows", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("ui.hide_builtin_workflows")
	}

	// Handle hide builtin presets
	if req.Msg.HideBuiltinPresets != nil {
		value := "false"
		if *req.Msg.HideBuiltinPresets {
			value = "true"
		}
		if err := s.upsertSetting(ctx, userID, nil, "ui.hide_builtin_presets", value); err != nil {
			logging.Error("Failed to update ui.hide_builtin_presets", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update preferences"))
		}
		markChanged("ui.hide_builtin_presets")
	}

	if len(changedKeys) > 0 {
		sort.Strings(changedKeys)
		analyticsClient.TrackPreferencesUpdated(analytics.PreferencesUpdatedMetrics{
			ChangedKeys:                  changedKeys,
			ModelProviderSettingsChanged: modelProviderSettingsChanged,
		})
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after UpdatePreferences", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdatePreferencesResponse{
		Success: true,
		Message: "Preferences updated successfully",
	}), nil
}

// SetHiddenItem toggles the hidden state of a workflow or preset
func (s *SettingsService) SetHiddenItem(ctx context.Context, req *connect.Request[reliantv1.SetHiddenItemRequest]) (*connect.Response[reliantv1.SetHiddenItemResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Validate item type
	if req.Msg.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW && req.Msg.ItemType != reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_type must be 'workflow' or 'preset'"))
	}

	// Validate slug
	if req.Msg.Slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slug cannot be empty"))
	}

	// Update visibility override
	// hidden=true means is_visible=false, hidden=false means is_visible=true
	isVisible := !req.Msg.Hidden
	if err := s.database.SetVisibilityOverride(ctx, userID, int32(req.Msg.ItemType), req.Msg.Slug, isVisible); err != nil {
		logging.Error("Failed to set visibility override", "error", err, "type", req.Msg.ItemType, "slug", req.Msg.Slug)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update hidden state"))
	}

	action := "hidden"
	if !req.Msg.Hidden {
		action = "shown"
	}

	return connect.NewResponse(&reliantv1.SetHiddenItemResponse{
		Success: true,
		Message: fmt.Sprintf("%s %s successfully %s", req.Msg.ItemType, req.Msg.Slug, action),
	}), nil
}

// getEffectiveHiddenItems computes the list of hidden items for a user.
// This combines system defaults (items hidden by default) with user overrides.
func (s *SettingsService) getEffectiveHiddenItems(ctx context.Context, userID string, itemType int32) ([]string, error) {
	hiddenSet := make(map[string]bool)

	// Start with items hidden by default
	defaults, err := s.database.ListHiddenItemDefaults(ctx, itemType)
	if err != nil {
		return nil, fmt.Errorf("failed to list hidden item defaults: %w", err)
	}
	for _, slug := range defaults {
		hiddenSet[slug] = true
	}

	// Apply user overrides
	overrides, err := s.database.ListVisibilityOverrides(ctx, userID, itemType)
	if err != nil {
		return nil, fmt.Errorf("failed to list visibility overrides: %w", err)
	}
	for slug, isVisible := range overrides {
		if isVisible {
			// User explicitly made this visible
			delete(hiddenSet, slug)
		} else {
			// User explicitly hid this
			hiddenSet[slug] = true
		}
	}

	// Convert to slice
	result := make([]string, 0, len(hiddenSet))
	for slug := range hiddenSet {
		result = append(result, slug)
	}
	return result, nil
}

// ============================================================================
// Sub-phase 6c: Prompts & Active Settings
// ============================================================================

// GetPrompts retrieves global user prompts
func (s *SettingsService) GetPrompts(ctx context.Context, req *connect.Request[reliantv1.GetPromptsRequest]) (*connect.Response[reliantv1.GetPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.database.GetSetting(ctx, userID, nil, "global.prompts")
	if err != nil {
		// Return empty prompts if not found
		return connect.NewResponse(&reliantv1.GetPromptsResponse{
			Prompts: []*reliantv1.UserPrompt{},
		}), nil
	}

	var prompts []struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Content  string  `json:"content"`
		Default  *bool   `json:"default,omitempty"`
		Hotkey   *string `json:"hotkey,omitempty"`
		Category *string `json:"category,omitempty"`
	}

	if setting.Value != "" {
		if err := json.Unmarshal([]byte(setting.Value), &prompts); err != nil {
			logging.Error("Failed to unmarshal prompts", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse prompts"))
		}
	}

	protoPrompts := make([]*reliantv1.UserPrompt, len(prompts))
	for i, p := range prompts {
		protoPrompts[i] = &reliantv1.UserPrompt{
			Id:       p.ID,
			Title:    p.Title,
			Content:  p.Content,
			Default:  p.Default,
			Hotkey:   p.Hotkey,
			Category: p.Category,
		}
	}

	return connect.NewResponse(&reliantv1.GetPromptsResponse{
		Prompts: protoPrompts,
	}), nil
}

// SavePrompts saves global user prompts
func (s *SettingsService) SavePrompts(ctx context.Context, req *connect.Request[reliantv1.SavePromptsRequest]) (*connect.Response[reliantv1.SavePromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Convert proto prompts to JSON-serializable struct
	prompts := make([]map[string]interface{}, len(req.Msg.Prompts))
	for i, p := range req.Msg.Prompts {
		prompt := map[string]interface{}{
			"id":      p.Id,
			"title":   p.Title,
			"content": p.Content,
		}
		if p.Default != nil {
			prompt["default"] = *p.Default
		}
		if p.Hotkey != nil {
			prompt["hotkey"] = *p.Hotkey
		}
		if p.Category != nil {
			prompt["category"] = *p.Category
		}
		prompts[i] = prompt
	}

	promptsJSON, err := json.Marshal(prompts)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to serialize prompts"))
	}

	if err := s.upsertSetting(ctx, userID, nil, "global.prompts", string(promptsJSON)); err != nil {
		logging.Error("Failed to save prompts", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save prompts"))
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after SavePrompts", "error", err)
	}

	return connect.NewResponse(&reliantv1.SavePromptsResponse{
		Success: true,
		Message: "Prompts saved successfully",
		Prompts: req.Msg.Prompts,
	}), nil
}

// ============================================================================
// Sub-phase 6d: Provider Settings (API Keys)
// ============================================================================

// GetProviderStatuses retrieves configuration status of all providers
func (s *SettingsService) GetProviderStatuses(ctx context.Context, req *connect.Request[reliantv1.GetProviderStatusesRequest]) (*connect.Response[reliantv1.GetProviderStatusesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	keys, err := s.database.GetProviderAPIKeys(ctx, userID)
	if err != nil {
		logging.Error("Failed to get provider API keys", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get provider statuses"))
	}

	apiKeys := make(map[string]string, len(keys))
	for provider, key := range keys {
		if masked := maskStoredCredential(key); masked != nil {
			apiKeys[provider] = *masked
		}
	}

	providers := []models.Family{
		"claude",
		"codex",
		"openrouter",
		"anthropic",
		"openai",
		"gemini",
	}
	if features.IsReliantManagedAccessEnabledForContext(ctx, s.database, userID) {
		providers = append([]models.Family{"reliant"}, providers...)
	}

	providerDisplayNames := map[models.Family]string{
		"reliant":    "Reliant",
		"claude":     "Claude Code",
		"codex":      "Codex (ChatGPT)",
		"openrouter": "OpenRouter",
		"anthropic":  "Anthropic",
		"openai":     "OpenAI",
		"gemini":     "Google Gemini",
	}

	statuses := make([]*reliantv1.ProviderStatus, 0, len(providers))
	for _, provider := range providers {
		status := &reliantv1.ProviderStatus{
			Provider:    string(provider),
			DisplayName: providerDisplayNames[provider],
		}

		switch provider {
		case "reliant":
			status.AuthMethod = stringPtr("reliant")
			token, tokenErr := s.getReliantStoredToken(ctx, userID)
			if tokenErr != nil {
				logging.Warn("Failed to load Reliant provider token", "error", tokenErr)
				status.Status = stringPtr("internal")
				status.StatusMessage = stringPtr("Failed to load Reliant token")
				statuses = append(statuses, status)
				continue
			}

			status.Configured = token != ""
			status.HasApiKey = token != ""
			status.MaskedKey = maskStoredCredential(token)
			if token == "" {
				status.Status = stringPtr("not_configured")
				status.StatusMessage = stringPtr("Reliant is not configured")
				statuses = append(statuses, status)
				continue
			}

			if !s.hasControlPlaneClient() {
				status.Status = stringPtr("failed_precondition")
				status.StatusMessage = stringPtr(controlPlaneErrorMessage(controlplane.ErrNotConfigured, "Reliant control-plane client is not configured"))
				statuses = append(statuses, status)
				continue
			}

			access, accessErr := s.controlPlaneClient.GetCurrentLLMAccess(ctx, token)
			if accessErr != nil {
				accessStatus := reliantAccessStatusFromError(accessErr, "Failed to load Reliant access")
				status.Status = stringPtr(accessStatus.State)
				status.StatusMessage = stringPtr(accessStatus.Message)
			} else {
				accessStatus := reliantAccessStatusFromGrant(access)
				status.Status = stringPtr(accessStatus.State)
				status.StatusMessage = stringPtr(accessStatus.Message)
			}
		case "claude":
			status.AuthMethod = stringPtr("oauth")
			tokens, tokenErr := s.database.GetClaudeAuthTokens(ctx, userID)
			if tokenErr != nil {
				logging.Warn("Failed to load Claude auth tokens", "error", tokenErr)
				status.Status = stringPtr("internal")
				status.StatusMessage = stringPtr("Failed to load Claude credentials")
				statuses = append(statuses, status)
				continue
			}
			if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
				status.Status = stringPtr("not_configured")
				status.StatusMessage = stringPtr("Claude is not connected")
				statuses = append(statuses, status)
				continue
			}
			if claude.IsTokenExpired(tokens.ExpiresAt) {
				status.Status = stringPtr("expired")
				status.StatusMessage = stringPtr("Claude session expired. Please reconnect Claude.")
				statuses = append(statuses, status)
				continue
			}
			status.Configured = true
			status.HasApiKey = true
			status.Status = stringPtr("connected")
			status.StatusMessage = stringPtr("Connected to Claude")
		case "codex":
			status.AuthMethod = stringPtr("oauth")
			tokens, tokenErr := s.database.GetCodexAuthTokens(ctx, userID)
			if tokenErr != nil {
				logging.Warn("Failed to load Codex auth tokens", "error", tokenErr)
				status.Status = stringPtr("internal")
				status.StatusMessage = stringPtr("Failed to load Codex credentials")
				statuses = append(statuses, status)
				continue
			}
			if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
				status.Status = stringPtr("not_configured")
				status.StatusMessage = stringPtr("Codex is not connected")
				statuses = append(statuses, status)
				continue
			}
			if codex.IsTokenExpired(tokens.AccessToken) {
				status.Status = stringPtr("expired")
				status.StatusMessage = stringPtr("Codex session expired. Please reconnect Codex.")
				statuses = append(statuses, status)
				continue
			}
			status.Configured = true
			status.HasApiKey = true
			status.Status = stringPtr("connected")
			status.StatusMessage = stringPtr("Connected to Codex")
		default:
			status.AuthMethod = stringPtr("api_key")
			maskedKey, hasKey := apiKeys[string(provider)]
			status.Configured = hasKey
			status.HasApiKey = hasKey
			if hasKey {
				status.MaskedKey = &maskedKey
				status.Status = stringPtr("connected")
				status.StatusMessage = stringPtr("API key configured")
			} else {
				status.Status = stringPtr("not_configured")
				status.StatusMessage = stringPtr("API key not configured")
			}
		}

		statuses = append(statuses, status)
	}

	return connect.NewResponse(&reliantv1.GetProviderStatusesResponse{Providers: statuses}), nil
}

// UpdateProviderAPIKey updates or deletes an API key for a provider
func (s *SettingsService) UpdateProviderAPIKey(ctx context.Context, req *connect.Request[reliantv1.UpdateProviderAPIKeyRequest]) (*connect.Response[reliantv1.UpdateProviderAPIKeyResponse], error) {
	userID := auth.MustGetUserID(ctx)
	provider := req.Msg.Provider
	analyticsClient := analytics.GetClientForUser(ctx, userID)
	trackProviderEvent := func(action, authMethod string) {
		analyticsClient.TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
			Provider:   provider,
			Action:     action,
			AuthMethod: authMethod,
		})
		if action == "connected" || action == "updated" {
			existingKeys, err := s.database.GetProviderAPIKeys(ctx, userID)
			totalProviders := 0
			if err == nil {
				for _, v := range existingKeys {
					if v != "" {
						totalProviders++
					}
				}
			}
			analyticsClient.TrackAPIKeyConfigured(analytics.APIKeyConfiguredMetrics{
				Provider:       provider,
				AuthMethod:     authMethod,
				IsFirstKey:     totalProviders <= 1,
				TotalProviders: totalProviders,
			})
		}
	}

	validProviders := map[string]bool{
		"reliant":    true,
		"claude":     true,
		"codex":      true,
		"anthropic":  true,
		"openai":     true,
		"gemini":     true,
		"openrouter": true,
	}
	if !validProviders[provider] {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported provider: %s", provider))
	}

	if provider == "claude" {
		if strings.TrimSpace(req.Msg.ApiKey) != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Claude does not use manual API keys. Use Login with Claude in Settings"))
		}
		if err := s.database.DeleteClaudeAuthTokens(ctx, userID); err != nil {
			logging.Error("Failed to delete Claude auth tokens", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to disconnect from Claude"))
		}
		if err := s.database.DeleteProviderAPIKey(ctx, userID, "claude"); err != nil {
			logging.Warn("Failed to remove Claude provider marker", "error", err)
		}
		trackProviderEvent("disconnected", "oauth")
		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
			logging.Warn("Failed to emit config_health refetch after Claude disconnect", "error", err)
		}
		return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{Success: true, Message: "Disconnected from Claude"}), nil
	}

	if provider == "codex" {
		if strings.TrimSpace(req.Msg.ApiKey) != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("codex does not use manual API keys. Use Login with Codex in Settings"))
		}
		if err := s.database.DeleteCodexAuthTokens(ctx, userID); err != nil {
			logging.Error("Failed to delete Codex auth tokens", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to disconnect from Codex"))
		}
		if err := s.database.DeleteProviderAPIKey(ctx, userID, "codex"); err != nil {
			logging.Error("Failed to delete codex provider marker", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to disconnect from Codex"))
		}
		trackProviderEvent("disconnected", "oauth")
		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
			logging.Warn("Failed to emit config_health refetch after codex disconnect", "error", err)
		}
		return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{Success: true, Message: "Disconnected from Codex"}), nil
	}

	if provider == "reliant" {
		if err := s.ensureReliantManagedAccessEnabled(ctx, userID); err != nil {
			return nil, err
		}
		existingToken, err := s.getReliantStoredToken(ctx, userID)
		if err != nil {
			logging.Error("Failed to load existing Reliant token", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update Reliant token"))
		}
		token := strings.TrimSpace(req.Msg.ApiKey)
		if token == "" {
			if err := s.database.DeleteProviderAPIKey(ctx, userID, "reliant"); err != nil {
				logging.Error("Failed to delete Reliant provider token", "error", err)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to disconnect from Reliant"))
			}
			trackProviderEvent("disconnected", "reliant")
			if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
				logging.Warn("Failed to emit config_health refetch after Reliant disconnect", "error", err)
			}
			return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{Success: true, Message: "Disconnected from Reliant"}), nil
		}
		if !strings.HasPrefix(token, "cpat_") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Reliant tokens must start with cpat_"))
		}

		message := "Reliant token updated successfully"
		if s.hasControlPlaneClient() {
			access, accessErr := s.controlPlaneClient.GetCurrentLLMAccess(ctx, token)
			if accessErr != nil {
				if controlPlaneConnectCode(accessErr) == connect.CodeUnauthenticated {
					return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", controlPlaneErrorMessage(accessErr, "Invalid Reliant token")))
				}
				message = fmt.Sprintf("Reliant token saved. Current access status: %s", controlPlaneErrorMessage(accessErr, "Access unavailable"))
			} else if accessStatus := reliantAccessStatusFromGrant(access); accessStatus != nil {
				message = accessStatus.Message
			}
		} else {
			message = "Reliant token saved. Reliant control-plane client is not configured"
		}

		if err := s.database.SetProviderAPIKey(ctx, userID, "reliant", token); err != nil {
			logging.Error("Failed to save Reliant provider token", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save Reliant token"))
		}
		if existingToken == "" {
			trackProviderEvent("connected", "reliant")
		} else {
			trackProviderEvent("updated", "reliant")
		}
		if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
			logging.Warn("Failed to emit config_health refetch after Reliant token update", "error", err)
		}
		return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{Success: true, Message: message}), nil
	}

	if len(req.Msg.ApiKey) == 0 {
		if err := s.database.DeleteProviderAPIKey(ctx, userID, provider); err != nil {
			logging.Error("Failed to delete provider API key", "provider", provider, "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete API key"))
		}
		trackProviderEvent("deleted", "api_key")
	} else {
		valid, message := s.validateAPIKey(ctx, models.Family(provider), req.Msg.ApiKey)
		if !valid {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", message))
		}
		if err := s.database.SetProviderAPIKey(ctx, userID, provider, req.Msg.ApiKey); err != nil {
			logging.Error("Failed to set provider API key", "provider", provider, "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save API key"))
		}
		trackProviderEvent("updated", "api_key")
	}

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after UpdateProviderAPIKey", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdateProviderAPIKeyResponse{Success: true, Message: "API key updated successfully"}), nil
}

// ValidateProviderAPIKey validates an API key for a provider
func (s *SettingsService) ValidateProviderAPIKey(ctx context.Context, req *connect.Request[reliantv1.ValidateProviderAPIKeyRequest]) (*connect.Response[reliantv1.ValidateProviderAPIKeyResponse], error) {
	provider := models.Family(req.Msg.Provider)
	userID := auth.MustGetUserID(ctx)

	if provider == "reliant" {
		if err := s.ensureReliantManagedAccessEnabled(ctx, userID); err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "reliant managed access feature is disabled"}), nil
		}
		token := strings.TrimSpace(req.Msg.ApiKey)
		if token == "" {
			storedToken, err := s.getReliantStoredToken(ctx, userID)
			if err != nil {
				return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Failed to load Reliant token"}), nil
			}
			token = storedToken
		}
		if token == "" {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Reliant is not configured"}), nil
		}
		if !strings.HasPrefix(token, "cpat_") {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Reliant tokens must start with cpat_"}), nil
		}
		if !s.hasControlPlaneClient() {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Reliant control-plane client is not configured"}), nil
		}
		access, err := s.controlPlaneClient.GetCurrentLLMAccess(ctx, token)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: controlPlaneErrorMessage(err, "Failed to validate Reliant token")}), nil
		}
		if accessStatus := reliantAccessStatusFromGrant(access); accessStatus != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: accessStatus.Message}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: "Connected to Reliant"}), nil
	}

	if provider == "claude" {
		tokens, err := s.database.GetClaudeAuthTokens(ctx, userID)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Failed to load Claude credentials"}), nil
		}
		if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Claude is not connected"}), nil
		}
		if claude.IsTokenExpired(tokens.ExpiresAt) {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Claude session expired. Please reconnect Claude."}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: "Connected to Claude"}), nil
	}

	if provider == "codex" {
		tokens, err := s.database.GetCodexAuthTokens(ctx, userID)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Failed to load Codex credentials"}), nil
		}
		if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Codex is not connected"}), nil
		}
		if codex.IsTokenExpired(tokens.AccessToken) {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Codex session expired. Please reconnect Codex."}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: "Connected to Codex"}), nil
	}

	valid, message := s.validateAPIKey(ctx, provider, req.Msg.ApiKey)
	return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: valid, Message: message}), nil
}

func (s *SettingsService) CreateReliantProviderToken(ctx context.Context, req *connect.Request[reliantv1.CreateReliantProviderTokenRequest]) (*connect.Response[reliantv1.CreateReliantProviderTokenResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureReliantManagedAccessEnabled(ctx, userID); err != nil {
		return nil, err
	}
	userJWT, err := requireUserJWT(ctx)
	if err != nil {
		return nil, err
	}
	if !s.hasControlPlaneClient() {
		return nil, mapControlPlaneError(controlplane.ErrNotConfigured, "Reliant control-plane client is not configured")
	}
	if req.Msg.ExpiresInSeconds != nil && req.Msg.GetExpiresInSeconds() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("expires_in_seconds must be >= 0"))
	}

	cpReq := controlplane.CreateUserTokenRequest{}
	if req.Msg.Name != nil {
		cpReq.Name = strings.TrimSpace(req.Msg.GetName())
	}
	if req.Msg.Ephemeral != nil {
		cpReq.Ephemeral = req.Msg.GetEphemeral()
	}
	if req.Msg.ExpiresInSeconds != nil {
		cpReq.ExpiresInSeconds = req.Msg.GetExpiresInSeconds()
	}

	cpResp, err := s.controlPlaneClient.CreateUserToken(ctx, userJWT, cpReq)
	if err != nil {
		return nil, mapControlPlaneError(err, "failed to create Reliant token")
	}
	if cpResp == nil || strings.TrimSpace(cpResp.Token) == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("control-plane returned an empty Reliant token"))
	}

	token := strings.TrimSpace(cpResp.Token)
	if err := s.database.SetProviderAPIKey(ctx, userID, "reliant", token); err != nil {
		logging.Error("Failed to persist Reliant provider token", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save Reliant token"))
	}

	analyticsClient := analytics.GetClientForUser(ctx, userID)
	analyticsClient.TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{Provider: "reliant", Action: "connected", AuthMethod: "reliant"})
	if existingKeys, err := s.database.GetProviderAPIKeys(ctx, userID); err == nil {
		totalProviders := 0
		for _, value := range existingKeys {
			if value != "" {
				totalProviders++
			}
		}
		analyticsClient.TrackAPIKeyConfigured(analytics.APIKeyConfiguredMetrics{Provider: "reliant", AuthMethod: "reliant", IsFirstKey: totalProviders <= 1, TotalProviders: totalProviders})
	}
	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after Reliant token create", "error", err)
	}

	return connect.NewResponse(&reliantv1.CreateReliantProviderTokenResponse{
		Success:     true,
		Message:     "Reliant token created successfully",
		Token:       token,
		DaemonToken: reliantProviderTokenToProto(cpResp.DaemonToken),
	}), nil
}

func (s *SettingsService) ListReliantProviderTokens(ctx context.Context, req *connect.Request[reliantv1.ListReliantProviderTokensRequest]) (*connect.Response[reliantv1.ListReliantProviderTokensResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureReliantManagedAccessEnabled(ctx, userID); err != nil {
		return nil, err
	}
	userJWT, err := requireUserJWT(ctx)
	if err != nil {
		return nil, err
	}
	if !s.hasControlPlaneClient() {
		return nil, mapControlPlaneError(controlplane.ErrNotConfigured, "Reliant control-plane client is not configured")
	}

	tokens, err := s.controlPlaneClient.ListUserTokens(ctx, userJWT)
	if err != nil {
		return nil, mapControlPlaneError(err, "failed to list Reliant tokens")
	}
	protoTokens := make([]*reliantv1.ReliantProviderToken, 0, len(tokens))
	for _, token := range tokens {
		protoTokens = append(protoTokens, reliantProviderTokenToProto(token))
	}
	return connect.NewResponse(&reliantv1.ListReliantProviderTokensResponse{Tokens: protoTokens}), nil
}

func (s *SettingsService) RevokeReliantProviderToken(ctx context.Context, req *connect.Request[reliantv1.RevokeReliantProviderTokenRequest]) (*connect.Response[reliantv1.RevokeReliantProviderTokenResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureReliantManagedAccessEnabled(ctx, userID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Msg.TokenId) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("token_id is required"))
	}
	userJWT, err := requireUserJWT(ctx)
	if err != nil {
		return nil, err
	}
	if !s.hasControlPlaneClient() {
		return nil, mapControlPlaneError(controlplane.ErrNotConfigured, "Reliant control-plane client is not configured")
	}
	if err := s.controlPlaneClient.RevokeUserToken(ctx, userJWT, strings.TrimSpace(req.Msg.TokenId)); err != nil {
		return nil, mapControlPlaneError(err, "failed to revoke Reliant token")
	}

	message := "Reliant token revoked"
	if req.Msg.DeleteLocalCredential {
		if err := s.database.DeleteProviderAPIKey(ctx, userID, "reliant"); err != nil {
			logging.Error("Failed to delete local Reliant credential", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to remove local Reliant credential"))
		}
		analytics.GetClientForUser(ctx, userID).TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{Provider: "reliant", Action: "disconnected", AuthMethod: "reliant"})
		message = "Reliant token revoked and local credential removed"
	}
	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after Reliant token revoke", "error", err)
	}
	return connect.NewResponse(&reliantv1.RevokeReliantProviderTokenResponse{Success: true, Message: message}), nil
}

func (s *SettingsService) GetReliantProviderStatus(ctx context.Context, req *connect.Request[reliantv1.GetReliantProviderStatusRequest]) (*connect.Response[reliantv1.GetReliantProviderStatusResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureReliantManagedAccessEnabled(ctx, userID); err != nil {
		return nil, err
	}
	storedToken, err := s.getReliantStoredToken(ctx, userID)
	if err != nil {
		logging.Error("Failed to load stored Reliant token", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load Reliant provider status"))
	}

	resp := &reliantv1.GetReliantProviderStatusResponse{
		Configured:  storedToken != "",
		MaskedToken: maskStoredCredential(storedToken),
		Tokens:      []*reliantv1.ReliantProviderToken{},
	}

	if s.hasControlPlaneClient() {
		if userJWT, ok := auth.GetUserJWTFromContext(ctx); ok && strings.TrimSpace(userJWT) != "" {
			tokens, listErr := s.controlPlaneClient.ListUserTokens(ctx, strings.TrimSpace(userJWT))
			if listErr != nil {
				logging.Warn("Failed to list Reliant provider tokens", "error", listErr)
			} else {
				for _, token := range tokens {
					resp.Tokens = append(resp.Tokens, reliantProviderTokenToProto(token))
				}
			}
		}
	}

	if storedToken == "" {
		resp.Access = &reliantv1.ReliantProviderAccessStatus{State: "not_configured", Message: "Reliant is not configured"}
		return connect.NewResponse(resp), nil
	}
	if !s.hasControlPlaneClient() {
		resp.Access = reliantAccessStatusFromError(controlplane.ErrNotConfigured, "Reliant control-plane client is not configured")
		return connect.NewResponse(resp), nil
	}

	access, accessErr := s.controlPlaneClient.GetCurrentLLMAccess(ctx, storedToken)
	if accessErr != nil {
		resp.Access = reliantAccessStatusFromError(accessErr, "Failed to load Reliant access status")
	} else {
		resp.Access = reliantAccessStatusFromGrant(access)
	}
	return connect.NewResponse(resp), nil
}

func (s *SettingsService) CompleteCodexOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteCodexOAuthRequest]) (*connect.Response[reliantv1.CompleteCodexOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)

	if code == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("authorization code is required"))
	}
	if codeVerifier == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("code_verifier is required"))
	}
	if redirectURI == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("redirect_uri is required"))
	}

	tokens, err := codex.ExchangeCodexAuthorizationCode(code, codeVerifier, redirectURI)
	if err != nil {
		logging.Error("Codex OAuth code exchange failed", "error", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Codex OAuth: %w", err))
	}

	if err := s.database.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		AccountID:    tokens.AccountID,
	}); err != nil {
		logging.Error("Failed to persist Codex auth tokens", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save Codex credentials"))
	}

	if err := s.database.SetProviderAPIKey(ctx, userID, "codex", "oauth"); err != nil {
		logging.Error("Failed to set Codex provider marker", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to connect Codex provider"))
	}

	analytics.GetClientForUser(ctx, userID).TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
		Provider:   "codex",
		Action:     "connected",
		AuthMethod: "oauth",
	})

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after Codex OAuth connect", "error", err)
	}

	return connect.NewResponse(&reliantv1.CompleteCodexOAuthResponse{
		Success: true,
		Message: "Connected to Codex",
	}), nil
}

func (s *SettingsService) CompleteClaudeOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteClaudeOAuthRequest]) (*connect.Response[reliantv1.CompleteClaudeOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	state := strings.TrimSpace(req.Msg.State)

	if code == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("authorization code is required"))
	}
	if codeVerifier == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("code_verifier is required"))
	}
	if redirectURI == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("redirect_uri is required"))
	}

	tokens, err := claude.ExchangeClaudeAuthorizationCode(code, codeVerifier, redirectURI, state)
	if err != nil {
		logging.Error("Claude OAuth code exchange failed", "error", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to complete Claude OAuth: %w", err))
	}

	if err := s.database.SetClaudeAuthTokens(ctx, userID, db.ClaudeAuthTokens{
		AccessToken:      tokens.AccessToken,
		RefreshToken:     tokens.RefreshToken,
		ExpiresAt:        tokens.ExpiresAt,
		AccountUUID:      tokens.AccountUUID,
		AccountEmail:     tokens.AccountEmail,
		OrganizationUUID: tokens.OrganizationUUID,
		OrganizationName: tokens.OrganizationName,
		Scope:            tokens.Scope,
	}); err != nil {
		logging.Error("Failed to persist Claude auth tokens", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save Claude credentials"))
	}

	if err := s.database.SetProviderAPIKey(ctx, userID, "claude", "oauth"); err != nil {
		logging.Error("Failed to set Claude provider marker", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to connect Claude provider"))
	}

	analytics.GetClientForUser(ctx, userID).TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
		Provider:   "claude",
		Action:     "connected",
		AuthMethod: "oauth",
	})

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after Claude OAuth connect", "error", err)
	}

	return connect.NewResponse(&reliantv1.CompleteClaudeOAuthResponse{
		Success: true,
		Message: "Connected to Claude",
	}), nil
}

// ============================================================================
// Sub-phase 6e: Privacy Settings
// ============================================================================

// GetPrivacySettings retrieves user privacy settings
func (s *SettingsService) GetPrivacySettings(ctx context.Context, req *connect.Request[reliantv1.GetPrivacySettingsRequest]) (*connect.Response[reliantv1.GetPrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	analyticsEnabled, crashReportingEnabled, err := s.database.GetPrivacySettings(ctx, userID)
	if err != nil {
		logging.Error("Failed to get privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get privacy settings"))
	}

	return connect.NewResponse(&reliantv1.GetPrivacySettingsResponse{
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
	}), nil
}

// UpdatePrivacySettings updates user privacy settings
func (s *SettingsService) UpdatePrivacySettings(ctx context.Context, req *connect.Request[reliantv1.UpdatePrivacySettingsRequest]) (*connect.Response[reliantv1.UpdatePrivacySettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get current settings
	analyticsEnabled, crashReportingEnabled, err := s.database.GetPrivacySettings(ctx, userID)
	if err != nil {
		logging.Error("Failed to get current privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update privacy settings"))
	}

	// Update only the fields that were provided
	if req.Msg.AnalyticsEnabled != nil {
		analyticsEnabled = *req.Msg.AnalyticsEnabled
	}
	if req.Msg.CrashReportingEnabled != nil {
		crashReportingEnabled = *req.Msg.CrashReportingEnabled
	}

	// Save the updated settings
	if err := s.database.SetPrivacySettings(ctx, userID, analyticsEnabled, crashReportingEnabled); err != nil {
		logging.Error("Failed to save privacy settings", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save privacy settings"))
	}

	logging.Info("Privacy settings updated", "userID", userID, "analyticsEnabled", analyticsEnabled, "crashReportingEnabled", crashReportingEnabled)

	return connect.NewResponse(&reliantv1.UpdatePrivacySettingsResponse{
		Success:               true,
		Message:               "Privacy settings updated successfully",
		AnalyticsEnabled:      analyticsEnabled,
		CrashReportingEnabled: crashReportingEnabled,
		RequiresRestart:       false,
	}), nil
}

// TrackPageVisited tracks a page visit for analytics
func (s *SettingsService) TrackPageVisited(ctx context.Context, req *connect.Request[reliantv1.TrackPageVisitedRequest]) (*connect.Response[reliantv1.TrackPageVisitedResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get analytics client that respects user's privacy settings
	analyticsClient := analytics.GetClientForUser(ctx, userID)

	metrics := analytics.PageVisitedMetrics{
		PageName: req.Msg.PageName,
	}
	if req.Msg.PreviousPage != nil {
		metrics.PreviousPage = *req.Msg.PreviousPage
	}

	analyticsClient.TrackPageVisited(metrics)

	return connect.NewResponse(&reliantv1.TrackPageVisitedResponse{
		Success: true,
	}), nil
}

// TrackOnboardingEvent tracks an onboarding flow event for analytics.
// TODO: Uncomment after running `buf generate` to regenerate proto types.
//
// func (s *SettingsService) TrackOnboardingEvent(ctx context.Context, req *connect.Request[reliantv1.TrackOnboardingEventRequest]) (*connect.Response[reliantv1.TrackOnboardingEventResponse], error) {
// 	userID := auth.MustGetUserID(ctx)
// 	analyticsClient := analytics.GetClientForUser(ctx, userID)
//
// 	eventType := analytics.EventType(req.Msg.EventType)
// 	// Validate event type
// 	validTypes := map[analytics.EventType]bool{
// 		analytics.EventOnboardingStarted:       true,
// 		analytics.EventOnboardingStepCompleted: true,
// 		analytics.EventOnboardingStepSkipped:   true,
// 		analytics.EventOnboardingCompleted:     true,
// 		analytics.EventOnboardingSkipped:       true,
// 	}
// 	if !validTypes[eventType] {
// 		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid onboarding event type: %s", req.Msg.EventType))
// 	}
//
// 	metrics := analytics.OnboardingMetrics{
// 		TotalSteps:     int(req.Msg.TotalSteps),
// 		StepsCompleted: int(req.Msg.StepsCompleted),
// 		StepsSkipped:   int(req.Msg.StepsSkipped),
// 	}
// 	if req.Msg.StepId != nil {
// 		metrics.StepID = *req.Msg.StepId
// 	}
// 	if req.Msg.StepName != nil {
// 		metrics.StepName = *req.Msg.StepName
// 	}
//
// 	analyticsClient.TrackOnboardingEvent(eventType, metrics)
//
// 	return connect.NewResponse(&reliantv1.TrackOnboardingEventResponse{
// 		Success: true,
// 	}), nil
// }

// ============================================================================
// Skills Management
// ============================================================================

// InstallSkill installs or previews installation of a skill into Reliant-managed paths.
func (s *SettingsService) InstallSkill(
	ctx context.Context,
	req *connect.Request[reliantv1.InstallSkillRequest],
) (*connect.Response[reliantv1.InstallSkillResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureSkillsFeatureEnabled(ctx, userID); err != nil {
		return nil, err
	}

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if strings.TrimSpace(req.Msg.Source) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source is required"))
	}

	project, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	service := tools.NewSkillInstallerService()
	result, installErr := service.Install(ctx, tools.SkillInstallRequest{
		ProjectPath:    project.Path,
		Source:         req.Msg.Source,
		SourceSubpath:  req.Msg.GetSourceSubpath(),
		Ref:            req.Msg.GetRef(),
		Name:           req.Msg.GetName(),
		Scope:          skillScopeFromProto(req.Msg.GetScope()),
		ConflictPolicy: skillConflictPolicyFromProto(req.Msg.GetConflictPolicy()),
		DryRun:         req.Msg.DryRun,
	})
	if installErr != nil {
		msg := strings.TrimSpace(installErr.Error())
		if msg == "" {
			msg = "skill install operation failed"
		}
		return connect.NewResponse(&reliantv1.InstallSkillResponse{
			Success: false,
			Message: msg,
		}), nil
	}

	resultProto := &reliantv1.SkillInstallResult{
		Source:         result.Source,
		SourceType:     skillSourceTypeToProto(result.SourceType),
		ResolvedSource: result.ResolvedSource,
		TargetDir:      result.TargetDir,
		SkillName:      result.SkillName,
		InstallDirName: result.InstallDirName,
		InstalledFiles: append([]string(nil), result.InstalledFiles...),
		SkippedFiles:   append([]string(nil), result.SkippedFiles...),
		DryRun:         result.DryRun,
		Scope:          skillScopeToProto(result.Scope),
		ConflictPolicy: skillConflictPolicyToProto(result.ConflictPolicy),
	}
	if strings.TrimSpace(result.SourceSubpath) != "" {
		resultProto.SourceSubpath = &result.SourceSubpath
	}
	if strings.TrimSpace(result.GitRef) != "" {
		resultProto.GitRef = &result.GitRef
	}

	return connect.NewResponse(&reliantv1.InstallSkillResponse{
		Success: true,
		Message: tools.FormatSkillInstallSummary(result),
		Result:  resultProto,
	}), nil
}

// ListInstalledSkills returns installed skills discovered across project and global Reliant roots.
func (s *SettingsService) ListInstalledSkills(
	ctx context.Context,
	req *connect.Request[reliantv1.ListInstalledSkillsRequest],
) (*connect.Response[reliantv1.ListInstalledSkillsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureSkillsFeatureEnabled(ctx, userID); err != nil {
		return nil, err
	}

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	project, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	disabledDefinitionPathSet, err := loadDisabledSkillDefinitionPathSet(ctx, s.database, userID, project.ID)
	if err != nil {
		logging.Error("Failed to load disabled skill definitions", "error", err, "projectID", project.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list installed skills"))
	}

	discovered := skills.DefaultRuntime().Discover(ctx, skills.DiscoverInput{
		ProjectPath:               project.Path,
		DisabledDefinitionPathSet: disabledDefinitionPathSet,
		LoadFullDefinitions:       false,
	})
	discoveredAll := skills.DefaultRuntime().Discover(ctx, skills.DiscoverInput{
		ProjectPath:         project.Path,
		LoadFullDefinitions: false,
	})
	allItems := make([]*reliantv1.InstalledSkill, 0, len(discovered.Skills)+len(discovered.ShadowedBy)+len(disabledDefinitionPathSet))
	diagnostics := make([]*reliantv1.SkillDiscoveryDiagnostic, 0, len(discovered.Diagnostics))
	seenDefinitionPaths := make(map[string]struct{}, len(discovered.Skills)+len(discovered.ShadowedBy)+len(disabledDefinitionPathSet))
	discoveredAllByDefinitionPath := make(map[string]skills.Skill, len(discoveredAll.Skills))
	for _, skill := range discoveredAll.Skills {
		if !isReliantSkillScope(skill.Scope) {
			continue
		}
		definitionPath := skill.Path
		if skill.Scope == skills.ScopeBuiltin {
			definitionPath = filepath.ToSlash(filepath.Join(skill.SkillDir, "SKILL.md"))
		}
		canonicalDefinitionPath := skills.CanonicalDefinitionPath(definitionPath)
		if canonicalDefinitionPath == "" {
			continue
		}
		discoveredAllByDefinitionPath[canonicalDefinitionPath] = skill
	}
	for _, d := range discovered.Diagnostics {
		diagnostics = append(diagnostics, &reliantv1.SkillDiscoveryDiagnostic{
			Path:    d.Path,
			Scope:   skillScopeToProto(string(d.Scope)),
			Message: d.Message,
		})
	}

	for _, skill := range discovered.Skills {
		if !isReliantSkillScope(skill.Scope) {
			continue
		}
		definitionPath := skill.Path
		if skill.Scope == skills.ScopeBuiltin {
			definitionPath = filepath.ToSlash(filepath.Join(skill.SkillDir, "SKILL.md"))
		}
		canonicalDefinitionPath := skills.CanonicalDefinitionPath(definitionPath)
		if canonicalDefinitionPath == "" {
			continue
		}
		if len(disabledDefinitionPathSet) > 0 {
			if _, disabled := disabledDefinitionPathSet[canonicalDefinitionPath]; disabled {
				continue
			}
		}
		if _, exists := seenDefinitionPaths[canonicalDefinitionPath]; exists {
			continue
		}

		allItems = append(allItems, &reliantv1.InstalledSkill{
			SkillId:                  buildInstalledSkillID(skill.Scope, definitionPath, true),
			Name:                     skill.Name,
			Description:              skill.Description,
			Scope:                    skillScopeToProto(string(skill.Scope)),
			Format:                   skillFormatToProto(skill.Format),
			SkillDir:                 skill.SkillDir,
			DefinitionPath:           definitionPath,
			Active:                   true,
			ShadowedByDefinitionPath: nil,
		})
		seenDefinitionPaths[canonicalDefinitionPath] = struct{}{}
	}

	shadowedPaths := make([]string, 0, len(discovered.ShadowedBy))
	for shadowedPath := range discovered.ShadowedBy {
		shadowedPaths = append(shadowedPaths, shadowedPath)
	}
	sort.Strings(shadowedPaths)
	for _, shadowedPath := range shadowedPaths {
		winnerPath := discovered.ShadowedBy[shadowedPath]
		canonicalShadowedPath := skills.CanonicalDefinitionPath(shadowedPath)
		homeDir := s.getDaemonHomeDir(ctx, userID)
		resolvedScope, ok := inferReliantSkillScope(project.Path, shadowedPath, homeDir)
		if !ok {
			if skillAll, found := discoveredAllByDefinitionPath[canonicalShadowedPath]; found {
				resolvedScope = skillAll.Scope
				ok = true
			}
		}
		if !ok {
			continue
		}

		if len(disabledDefinitionPathSet) > 0 {
			if _, disabled := disabledDefinitionPathSet[canonicalShadowedPath]; disabled {
				continue
			}
			if _, winnerDisabled := disabledDefinitionPathSet[skills.CanonicalDefinitionPath(winnerPath)]; winnerDisabled {
				continue
			}
		}

		shadowedSkill, found := discoveredAllByDefinitionPath[canonicalShadowedPath]
		if !found {
			var (
				parseErr error
			)
			if resolvedScope == skills.ScopeBuiltin {
				canonicalPath := canonicalBuiltinDefinitionPath(shadowedPath)
				blob, readErr := skillcatalog.ReadBuiltinSkillDefinition(canonicalPath)
				if readErr != nil {
					diagnostics = append(diagnostics, &reliantv1.SkillDiscoveryDiagnostic{
						Path:    shadowedPath,
						Scope:   skillScopeToProto(string(resolvedScope)),
						Message: "failed to parse shadowed skill: " + readErr.Error(),
					})
					continue
				}
				definition, definitionErr := skillcatalog.ParseSkillMarkdown(canonicalPath, resolvedScope, blob)
				if definitionErr == nil {
					shadowedSkill = catalogDefinitionToSkill(definition)
					shadowedSkill.Files = nil
				}
				parseErr = definitionErr
			} else {
				blob, fileFound, readErr := s.readSkillFile(ctx, userID, shadowedPath)
				if readErr != nil || !fileFound {
					diagnostics = append(diagnostics, &reliantv1.SkillDiscoveryDiagnostic{
						Path:    shadowedPath,
						Scope:   skillScopeToProto(string(resolvedScope)),
						Message: "failed to read shadowed skill definition",
					})
					continue
				}
				shadowedSkill, parseErr = parseSkillFromContent(shadowedPath, resolvedScope, blob)
			}
			if parseErr != nil {
				diagnostics = append(diagnostics, &reliantv1.SkillDiscoveryDiagnostic{
					Path:    shadowedPath,
					Scope:   skillScopeToProto(string(resolvedScope)),
					Message: "failed to parse shadowed skill: " + parseErr.Error(),
				})
				continue
			}
		}
		if !isReliantSkillScope(shadowedSkill.Scope) {
			continue
		}

		definitionPath := shadowedSkill.Path
		if shadowedSkill.Scope == skills.ScopeBuiltin {
			definitionPath = filepath.ToSlash(filepath.Join(shadowedSkill.SkillDir, "SKILL.md"))
		}
		canonicalDefinitionPath := skills.CanonicalDefinitionPath(definitionPath)
		if canonicalDefinitionPath == "" {
			continue
		}
		if _, exists := seenDefinitionPaths[canonicalDefinitionPath]; exists {
			continue
		}

		winner := winnerPath
		allItems = append(allItems, &reliantv1.InstalledSkill{
			SkillId:                  buildInstalledSkillID(shadowedSkill.Scope, definitionPath, false),
			Name:                     shadowedSkill.Name,
			Description:              shadowedSkill.Description,
			Scope:                    skillScopeToProto(string(shadowedSkill.Scope)),
			Format:                   skillFormatToProto(shadowedSkill.Format),
			SkillDir:                 shadowedSkill.SkillDir,
			DefinitionPath:           definitionPath,
			Active:                   false,
			ShadowedByDefinitionPath: &winner,
		})
		seenDefinitionPaths[canonicalDefinitionPath] = struct{}{}
	}

	if len(disabledDefinitionPathSet) > 0 {
		disabledPaths := make([]string, 0, len(disabledDefinitionPathSet))
		for definitionPath := range disabledDefinitionPathSet {
			disabledPaths = append(disabledPaths, definitionPath)
		}
		sort.Strings(disabledPaths)

		for _, disabledPath := range disabledPaths {
			canonicalDisabledPath := skills.CanonicalDefinitionPath(disabledPath)
			if canonicalDisabledPath == "" {
				continue
			}
			if _, exists := seenDefinitionPaths[canonicalDisabledPath]; exists {
				continue
			}

			disabledSkill, found := discoveredAllByDefinitionPath[canonicalDisabledPath]
			if !found || !isReliantSkillScope(disabledSkill.Scope) {
				continue
			}

			definitionPath := disabledSkill.Path
			if disabledSkill.Scope == skills.ScopeBuiltin {
				definitionPath = filepath.ToSlash(filepath.Join(disabledSkill.SkillDir, "SKILL.md"))
			}
			canonicalDefinitionPath := skills.CanonicalDefinitionPath(definitionPath)
			if canonicalDefinitionPath == "" {
				continue
			}
			if _, exists := seenDefinitionPaths[canonicalDefinitionPath]; exists {
				continue
			}

			var winner *string
			if winnerPath, ok := discoveredAll.ShadowedBy[disabledSkill.Path]; ok {
				w := winnerPath
				winner = &w
			}

			allItems = append(allItems, &reliantv1.InstalledSkill{
				SkillId:                  buildInstalledSkillID(disabledSkill.Scope, definitionPath, false),
				Name:                     disabledSkill.Name,
				Description:              disabledSkill.Description,
				Scope:                    skillScopeToProto(string(disabledSkill.Scope)),
				Format:                   skillFormatToProto(disabledSkill.Format),
				SkillDir:                 disabledSkill.SkillDir,
				DefinitionPath:           definitionPath,
				Active:                   false,
				ShadowedByDefinitionPath: winner,
			})
			seenDefinitionPaths[canonicalDefinitionPath] = struct{}{}
		}
	}

	sort.Slice(allItems, func(i, j int) bool {
		if allItems[i].Active != allItems[j].Active {
			return allItems[i].Active
		}
		if allItems[i].Name != allItems[j].Name {
			return allItems[i].Name < allItems[j].Name
		}
		if allItems[i].Scope != allItems[j].Scope {
			return allItems[i].Scope < allItems[j].Scope
		}
		return allItems[i].DefinitionPath < allItems[j].DefinitionPath
	})
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Scope != diagnostics[j].Scope {
			return diagnostics[i].Scope < diagnostics[j].Scope
		}
		if diagnostics[i].Path != diagnostics[j].Path {
			return diagnostics[i].Path < diagnostics[j].Path
		}
		return diagnostics[i].Message < diagnostics[j].Message
	})

	total := len(allItems)
	pageSize := parsePageSize(req.Msg.GetPageSize(), 100, 500)
	offset, pageErr := parsePageOffset(req.Msg.GetPageToken(), total)
	if pageErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, pageErr)
	}
	end := offset + pageSize
	if end > total {
		end = total
	}
	items := allItems[offset:end]

	return connect.NewResponse(&reliantv1.ListInstalledSkillsResponse{
		Skills:        items,
		Total:         int32(total),
		Diagnostics:   diagnostics,
		NextPageToken: buildNextPageToken(end, total),
	}), nil
}

// GetInstalledSkillDefinition returns full SKILL.md content for a discovered skill summary entry.
func (s *SettingsService) GetInstalledSkillDefinition(
	ctx context.Context,
	req *connect.Request[reliantv1.GetInstalledSkillDefinitionRequest],
) (*connect.Response[reliantv1.GetInstalledSkillDefinitionResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureSkillsFeatureEnabled(ctx, userID); err != nil {
		return nil, err
	}

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if strings.TrimSpace(req.Msg.SkillId) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("skill_id is required"))
	}
	project, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	disabledDefinitionPathSet, err := loadDisabledSkillDefinitionPathSet(ctx, s.database, userID, project.ID)
	if err != nil {
		logging.Error("Failed to load disabled skill definitions", "error", err, "projectID", project.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load skill definition"))
	}

	skill, _, resolveErr := s.resolveInstalledSkillByID(ctx, userID, project.Path, req.Msg.SkillId, disabledDefinitionPathSet)
	if resolveErr != nil {
		if connectErr, ok := resolveErr.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, resolveErr)
	}

	var (
		blob    []byte
		readErr error
	)
	if skill.Scope == skills.ScopeBuiltin {
		blob, readErr = skillcatalog.ReadBuiltinSkillDefinition(skill.Path)
	} else {
		var found bool
		blob, found, readErr = s.readSkillFile(ctx, userID, skill.Path)
		if readErr == nil && !found {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("skill definition not found: %s", skill.Path))
		}
	}
	if readErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read skill definition: %w", readErr))
	}

	assets := s.collectPackagedSkillImageAssets(ctx, userID, skill)

	return connect.NewResponse(&reliantv1.GetInstalledSkillDefinitionResponse{
		SkillId:           req.Msg.SkillId,
		DefinitionPath:    skill.Path,
		DefinitionContent: string(blob),
		Assets:            assets,
	}), nil
}

// SetSkillEnabled toggles whether an installed skill definition path participates in discovery.
func (s *SettingsService) SetSkillEnabled(
	ctx context.Context,
	req *connect.Request[reliantv1.SetSkillEnabledRequest],
) (*connect.Response[reliantv1.SetSkillEnabledResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureSkillsFeatureEnabled(ctx, userID); err != nil {
		return nil, err
	}

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if strings.TrimSpace(req.Msg.SkillId) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("skill_id is required"))
	}

	project, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	disabledSet, err := loadDisabledSkillDefinitionPathSet(ctx, s.database, userID, project.ID)
	if err != nil {
		logging.Error("Failed to load disabled skill definitions", "error", err, "projectID", project.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update skill state"))
	}

	resolveDisabledSet := disabledSet
	if req.Msg.Enabled {
		// Re-enable must resolve from current discovery regardless of disabled set membership.
		resolveDisabledSet = nil
	}

	skill, _, resolveErr := s.resolveInstalledSkillByID(ctx, userID, project.Path, req.Msg.SkillId, resolveDisabledSet)
	if resolveErr != nil {
		if connectErr, ok := resolveErr.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, resolveErr)
	}

	if skill.Scope == skills.ScopeBuiltin {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("builtin skills cannot be disabled"))
	}

	targetPath := skills.CanonicalDefinitionPath(skill.Path)
	if targetPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid skill definition path"))
	}

	if disabledSet == nil {
		disabledSet = make(map[string]struct{})
	}

	if req.Msg.Enabled {
		delete(disabledSet, targetPath)
	} else {
		disabledSet[targetPath] = struct{}{}
	}

	if err := s.upsertSetting(
		ctx,
		userID,
		&project.ID,
		skills.DisabledDefinitionPathsSettingKey,
		encodeDisabledSkillDefinitionPaths(disabledSet),
	); err != nil {
		logging.Error("Failed to persist disabled skill definitions", "error", err, "projectID", project.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update skill state"))
	}

	skillcatalog.DefaultCatalogIndex().Invalidate(project.Path)

	stateWord := "disabled"
	if req.Msg.Enabled {
		stateWord = "enabled"
	}

	return connect.NewResponse(&reliantv1.SetSkillEnabledResponse{
		Success: true,
		Message: fmt.Sprintf("Skill %s", stateWord),
		SkillId: req.Msg.SkillId,
		Enabled: req.Msg.Enabled,
	}), nil
}

// ListRecommendedSkills returns backend-defined recommended skills.
func (s *SettingsService) ListRecommendedSkills(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRecommendedSkillsRequest],
) (*connect.Response[reliantv1.ListRecommendedSkillsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureSkillsFeatureEnabled(ctx, userID); err != nil {
		return nil, err
	}

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if _, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	recommendedConfig, err := config.LoadRecommendedSkills()
	if err != nil {
		logging.Error("Failed to load recommended skills", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load recommended skills: %w", err))
	}
	if err := recommendedConfig.Validate(); err != nil {
		logging.Error("Invalid recommended skills configuration", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("invalid recommended skills configuration: %w", err))
	}

	allRecommended := make([]*reliantv1.RecommendedSkill, 0, len(recommendedConfig.Recommended))
	for _, rec := range recommendedConfig.Recommended {
		item := &reliantv1.RecommendedSkill{
			Id:          rec.ID,
			Name:        rec.Name,
			Description: rec.Description,
			Source:      rec.Source,
		}
		if rec.SourceSubpath != nil && strings.TrimSpace(*rec.SourceSubpath) != "" {
			item.SourceSubpath = rec.SourceSubpath
		}
		if rec.Ref != nil && strings.TrimSpace(*rec.Ref) != "" {
			item.Ref = rec.Ref
		}
		if rec.BundledBy != nil && strings.TrimSpace(*rec.BundledBy) != "" {
			item.BundledBy = rec.BundledBy
		}

		allRecommended = append(allRecommended, item)
	}
	sort.Slice(allRecommended, func(i, j int) bool {
		if allRecommended[i].Name != allRecommended[j].Name {
			return allRecommended[i].Name < allRecommended[j].Name
		}
		if allRecommended[i].Id != allRecommended[j].Id {
			return allRecommended[i].Id < allRecommended[j].Id
		}
		return allRecommended[i].Source < allRecommended[j].Source
	})

	total := len(allRecommended)
	pageSize := parsePageSize(req.Msg.GetPageSize(), 100, 500)
	offset, pageErr := parsePageOffset(req.Msg.GetPageToken(), total)
	if pageErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, pageErr)
	}
	end := offset + pageSize
	if end > total {
		end = total
	}

	return connect.NewResponse(&reliantv1.ListRecommendedSkillsResponse{
		Recommended:   allRecommended[offset:end],
		Total:         int32(total),
		NextPageToken: buildNextPageToken(end, total),
	}), nil
}

// DeleteGlobalSkill deletes a skill directory under ~/.reliant/skills.
func (s *SettingsService) DeleteGlobalSkill(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteGlobalSkillRequest],
) (*connect.Response[reliantv1.DeleteGlobalSkillResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if err := s.ensureSkillsFeatureEnabled(ctx, userID); err != nil {
		return nil, err
	}

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	project, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	relPath, err := normalizeGlobalSkillRelativePath(req.Msg.RelativePath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var deleteResp struct {
		Success           bool   `json:"success"`
		Error             string `json:"error,omitempty"`
		ErrorCode         string `json:"error_code,omitempty"`
		DefinitionContent string `json:"definition_content,omitempty"`
	}
	if err := s.sendDaemonCommand(ctx, userID, "skills.delete_global", map[string]string{"relative_path": relPath}, &deleteResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("daemon command failed: %w", err))
	}

	if !deleteResp.Success {
		switch deleteResp.ErrorCode {
		case "not_found":
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s", deleteResp.Error))
		case "permission_denied":
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s", deleteResp.Error))
		case "invalid_argument":
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", deleteResp.Error))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", deleteResp.Error))
		}
	}

	// Validate the definition content before confirming delete
	if deleteResp.DefinitionContent != "" {
		if _, parseErr := skillcatalog.ParseSkillMarkdown(relPath, skills.ScopeGlobal, []byte(deleteResp.DefinitionContent)); parseErr != nil {
			logging.Warn("Deleted global skill had invalid definition", "relativePath", relPath, "error", parseErr)
		}
	}

	if project != nil {
		skillcatalog.DefaultCatalogIndex().Invalidate(project.Path)
	}

	logging.Info("Global skill deleted successfully", "relativePath", relPath)
	return connect.NewResponse(&reliantv1.DeleteGlobalSkillResponse{
		Success: true,
		Message: "Deleted successfully",
	}), nil
}

// ============================================================================
// Configuration Health
// ============================================================================

// GetConfigHealth retrieves configuration errors and warnings.
// Collects errors from preset, workflow, and MCP loading.
func (s *SettingsService) GetConfigHealth(
	ctx context.Context,
	req *connect.Request[reliantv1.GetConfigHealthRequest],
) (*connect.Response[reliantv1.GetConfigHealthResponse], error) {
	collector := validation.Global()

	// Get all errors from the collector
	errors := collector.Errors()

	// Apply type filter if specified
	if req.Msg.TypeFilter != nil && *req.Msg.TypeFilter != "" {
		filtered := make([]*validation.Error, 0)
		for _, err := range errors {
			if string(err.Category) == *req.Msg.TypeFilter {
				filtered = append(filtered, err)
			}
		}
		errors = filtered
	}

	// Convert to proto
	protoErrors := make([]*reliantv1.ConfigError, 0, len(errors))
	var errorCount, warningCount int32

	for _, err := range errors {
		protoErr := &reliantv1.ConfigError{
			Type:     string(err.Category),
			Source:   err.Source,
			Message:  err.Message,
			Severity: configSeverityFromString(string(err.Severity)),
		}

		// Convert details map
		if err.Details != nil {
			protoErr.Details = make(map[string]string)
			for k, v := range err.Details {
				if strVal, ok := v.(string); ok {
					protoErr.Details[k] = strVal
				} else {
					// Convert non-string values to JSON
					if jsonBytes, jsonErr := json.Marshal(v); jsonErr == nil {
						protoErr.Details[k] = string(jsonBytes)
					}
				}
			}
		}

		protoErrors = append(protoErrors, protoErr)

		if err.Severity == validation.SeverityError {
			errorCount++
		} else {
			warningCount++
		}
	}

	return connect.NewResponse(&reliantv1.GetConfigHealthResponse{
		Errors:       protoErrors,
		ErrorCount:   errorCount,
		WarningCount: warningCount,
	}), nil
}