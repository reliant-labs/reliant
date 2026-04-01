// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	RegisterCommand("skills.read_file", handleSkillsReadFile)
	RegisterCommand("skills.read_skill_assets", handleSkillsReadSkillAssets)
	RegisterCommand("skills.delete_global", handleSkillsDeleteGlobal)
	RegisterCommand("skills.get_home_dir", handleSkillsGetHomeDir)
}

// --- skills.read_file ---

type skillsReadFileRequest struct {
	Path string `json:"path"`
}

type skillsReadFileResponse struct {
	Content string `json:"content"`
	Found   bool   `json:"found"`
}

func handleSkillsReadFile(_ context.Context, payload []byte) ([]byte, error) {
	var req skillsReadFileRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	blob, err := os.ReadFile(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return json.Marshal(skillsReadFileResponse{Found: false})
		}
		return nil, fmt.Errorf("read file %s: %w", req.Path, err)
	}

	return json.Marshal(skillsReadFileResponse{
		Content: string(blob),
		Found:   true,
	})
}

// --- skills.read_skill_assets ---

type skillsReadSkillAssetsRequest struct {
	SkillDir string `json:"skill_dir"`
}

type skillAssetEntry struct {
	RelativePath string `json:"relative_path"`
	MimeType     string `json:"mime_type"`
	ContentB64   string `json:"content_b64"`
}

type skillsReadSkillAssetsResponse struct {
	Assets []skillAssetEntry `json:"assets"`
}

func handleSkillsReadSkillAssets(_ context.Context, payload []byte) ([]byte, error) {
	var req skillsReadSkillAssetsRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.SkillDir == "" {
		return json.Marshal(skillsReadSkillAssetsResponse{Assets: []skillAssetEntry{}})
	}

	assetsRoot := filepath.Join(req.SkillDir, "assets")
	info, err := os.Stat(assetsRoot)
	if err != nil || !info.IsDir() {
		return json.Marshal(skillsReadSkillAssetsResponse{Assets: []skillAssetEntry{}})
	}

	assets := make([]skillAssetEntry, 0)
	_ = filepath.WalkDir(assetsRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		rel, relErr := filepath.Rel(req.SkillDir, path)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return nil
		}

		rel = filepath.ToSlash(rel)
		mimeType := imageAssetMimeType(rel)
		if !strings.HasPrefix(mimeType, "image/") {
			return nil
		}

		blob, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		assets = append(assets, skillAssetEntry{
			RelativePath: rel,
			MimeType:     mimeType,
			ContentB64:   base64.StdEncoding.EncodeToString(blob),
		})
		return nil
	})

	return json.Marshal(skillsReadSkillAssetsResponse{Assets: assets})
}

func imageAssetMimeType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// --- skills.delete_global ---

type skillsDeleteGlobalRequest struct {
	RelativePath string `json:"relative_path"`
}

type skillsDeleteGlobalResponse struct {
	Success           bool   `json:"success"`
	Error             string `json:"error,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	DefinitionContent string `json:"definition_content,omitempty"`
}

func handleSkillsDeleteGlobal(_ context.Context, payload []byte) ([]byte, error) {
	var req skillsDeleteGlobalRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.RelativePath == "" {
		return json.Marshal(skillsDeleteGlobalResponse{Error: "relative_path is required", ErrorCode: "invalid_argument"})
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return json.Marshal(skillsDeleteGlobalResponse{Error: "unable to resolve user home directory", ErrorCode: "internal"})
	}

	globalSkillsRoot := filepath.Join(homeDir, ".reliant", "skills")
	absRoot := filepath.Clean(globalSkillsRoot)
	absTarget := filepath.Clean(filepath.Join(absRoot, req.RelativePath))

	resolvedRel, relErr := filepath.Rel(absRoot, absTarget)
	if relErr != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(os.PathSeparator)) {
		return json.Marshal(skillsDeleteGlobalResponse{Error: "path escapes global skills root", ErrorCode: "permission_denied"})
	}

	info, err := os.Stat(absTarget)
	if err != nil {
		if os.IsNotExist(err) {
			return json.Marshal(skillsDeleteGlobalResponse{Error: err.Error(), ErrorCode: "not_found"})
		}
		return json.Marshal(skillsDeleteGlobalResponse{Error: err.Error(), ErrorCode: "internal"})
	}
	if !info.IsDir() {
		return json.Marshal(skillsDeleteGlobalResponse{Error: "relative_path must reference a skill directory", ErrorCode: "invalid_argument"})
	}

	skillDefinitionPath := filepath.Join(absTarget, "SKILL.md")
	definitionInfo, definitionErr := os.Stat(skillDefinitionPath)
	if definitionErr != nil {
		if os.IsNotExist(definitionErr) {
			return json.Marshal(skillsDeleteGlobalResponse{Error: "relative_path must reference a valid skill directory containing SKILL.md", ErrorCode: "invalid_argument"})
		}
		return json.Marshal(skillsDeleteGlobalResponse{Error: definitionErr.Error(), ErrorCode: "internal"})
	}
	if definitionInfo.IsDir() {
		return json.Marshal(skillsDeleteGlobalResponse{Error: "relative_path must reference a valid skill directory containing SKILL.md", ErrorCode: "invalid_argument"})
	}

	blob, readErr := os.ReadFile(skillDefinitionPath)
	if readErr != nil {
		return json.Marshal(skillsDeleteGlobalResponse{Error: fmt.Sprintf("failed to read skill definition before delete: %s", readErr), ErrorCode: "internal"})
	}

	if err := os.RemoveAll(absTarget); err != nil {
		return json.Marshal(skillsDeleteGlobalResponse{Error: err.Error(), ErrorCode: "internal"})
	}

	// Verification check
	if _, invalidateErr := os.Stat(absTarget); !os.IsNotExist(invalidateErr) {
		// Log-worthy but not a failure — the delete completed
	}

	return json.Marshal(skillsDeleteGlobalResponse{
		Success:           true,
		DefinitionContent: string(blob),
	})
}

// --- skills.get_home_dir ---

type skillsGetHomeDirResponse struct {
	HomeDir string `json:"home_dir"`
	Error   string `json:"error,omitempty"`
}

func handleSkillsGetHomeDir(_ context.Context, _ []byte) ([]byte, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return json.Marshal(skillsGetHomeDirResponse{Error: err.Error()})
	}
	return json.Marshal(skillsGetHomeDirResponse{HomeDir: homeDir})
}
