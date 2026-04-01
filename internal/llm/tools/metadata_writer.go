// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
	"gopkg.in/yaml.v3"
)

// MetadataWriterParams defines the parameters for metadata operations
type MetadataWriterParams struct {
	Action   string             `json:"action" jsonschema:"required,enum=write,enum=update,enum=validate,enum=read,description=Action to perform"`
	Metadata config.ProjectMeta `json:"metadata,omitempty" jsonschema:"description=Project metadata object (for write/update actions)"`
}

// MetadataWriterTool provides project metadata management capabilities
type MetadataWriterTool struct{}

// NewMetadataWriterTool creates a new metadata writer tool
func NewMetadataWriterTool() Tool {
	tool := &MetadataWriterTool{}
	return NewToolWrapper[MetadataWriterParams, interface{}](tool)
}

func (t *MetadataWriterTool) Name() string {
	return "metadata_writer"
}

func (t *MetadataWriterTool) Description() string {
	return "Writes and updates project metadata YAML file"
}

func (t *MetadataWriterTool) RequiresPermission(params MetadataWriterParams) (bool, error) {
	// metadata_writer tool requires permissions as it writes to metadata files
	return true, nil
}

func (t *MetadataWriterTool) Execute(rctx *rctx.ToolContext, params MetadataWriterParams) (interface{}, error) {
	// Require project path from context
	if rctx.Project == nil || rctx.Project.Path == "" {
		return nil, fmt.Errorf("no project context available")
	}

	path := filepath.Join(rctx.Project.Path, ".reliant", "project-meta.yaml")

	switch params.Action {
	case "write":
		return t.writeMetadata(params.Metadata, path)
	case "update":
		return t.updateMetadata(params.Metadata, path)
	case "validate":
		return t.validateMetadata(path)
	case "read":
		return t.readMetadata(path)
	default:
		return nil, fmt.Errorf("unknown action: %s", params.Action)
	}
}

func (t *MetadataWriterTool) writeMetadata(meta config.ProjectMeta, path string) (map[string]interface{}, error) {
	// Convert the metadata parameter to ProjectMeta struct
	// Validate the metadata
	if err := meta.Validate(); err != nil {
		return nil, fmt.Errorf("metadata validation failed: %w", err)
	}

	// Save the metadata
	if err := config.SaveProjectMetaTo(&meta, path); err != nil {
		return nil, fmt.Errorf("failed to save metadata: %w", err)
	}

	logging.Info(fmt.Sprintf("Project metadata written to %s", path))

	return map[string]interface{}{
		"success": true,
		"path":    path,
		"message": "Project metadata successfully written",
	}, nil
}

func (t *MetadataWriterTool) updateMetadata(metadataParam config.ProjectMeta, path string) (map[string]interface{}, error) {
	// Load existing metadata
	existing, err := config.LoadProjectMetaFrom(path)
	if err != nil {
		// If file doesn't exist, treat as write
		if os.IsNotExist(err) {
			return t.writeMetadata(metadataParam, path)
		}
		return nil, fmt.Errorf("failed to load existing metadata: %w", err)
	}

	// Convert the update parameter
	// Merge updates into existing
	t.mergeMetadata(existing, &metadataParam)

	// Validate the merged metadata
	if err := existing.Validate(); err != nil {
		return nil, fmt.Errorf("merged metadata validation failed: %w", err)
	}

	// Save the updated metadata
	if err := config.SaveProjectMetaTo(existing, path); err != nil {
		return nil, fmt.Errorf("failed to save updated metadata: %w", err)
	}

	logging.Info(fmt.Sprintf("Project metadata updated at %s", path))

	return map[string]interface{}{
		"success": true,
		"path":    path,
		"message": "Project metadata successfully updated",
	}, nil
}

func (t *MetadataWriterTool) validateMetadata(path string) (map[string]interface{}, error) {
	meta, err := config.LoadProjectMetaFrom(path)
	if err != nil {
		return map[string]interface{}{
			"valid":   false,
			"message": fmt.Sprintf("Failed to load metadata: %v", err),
		}, nil
	}

	if err := meta.Validate(); err != nil {
		return map[string]interface{}{
			"valid":   false,
			"message": fmt.Sprintf("Validation failed: %v", err),
		}, nil
	}

	return map[string]interface{}{
		"valid":        true,
		"message":      "Metadata is valid",
		"version":      meta.Version,
		"project":      meta.Project.Name,
		"type":         meta.Project.Type,
		"applications": len(meta.Applications),
	}, nil
}

func (t *MetadataWriterTool) readMetadata(path string) (interface{}, error) {
	meta, err := config.LoadProjectMetaFrom(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	// Convert to map for JSON serialization
	data, err := yaml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	var result map[string]interface{}
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata to map: %w", err)
	}

	return result, nil
}

func (t *MetadataWriterTool) mergeMetadata(existing, updates *config.ProjectMeta) {
	// Update project info if provided
	if updates.Project.Name != "" {
		existing.Project.Name = updates.Project.Name
	}
	if updates.Project.Type != "" {
		existing.Project.Type = updates.Project.Type
	}
	if updates.Project.Root != "" {
		existing.Project.Root = updates.Project.Root
	}

	// Merge global config
	if len(updates.Project.Global.Tests) > 0 {
		existing.Project.Global.Tests = updates.Project.Global.Tests
	}
	if len(updates.Project.Global.Linters) > 0 {
		existing.Project.Global.Linters = updates.Project.Global.Linters
	}
	if updates.Project.Global.Metadata != nil {
		if existing.Project.Global.Metadata == nil {
			existing.Project.Global.Metadata = make(map[string]interface{})
		}
		for k, v := range updates.Project.Global.Metadata {
			existing.Project.Global.Metadata[k] = v
		}
	}

	// Merge applications
	if len(updates.Applications) > 0 {
		// Create a map of existing applications for quick lookup
		appMap := make(map[string]*config.Application)
		for i := range existing.Applications {
			appMap[existing.Applications[i].ID] = &existing.Applications[i]
		}

		// Update or add applications
		for _, updatedApp := range updates.Applications {
			if existingApp, found := appMap[updatedApp.ID]; found {
				// Update existing application
				t.mergeApplication(existingApp, &updatedApp)
			} else {
				// Add new application
				existing.Applications = append(existing.Applications, updatedApp)
			}
		}
	}
}

func (t *MetadataWriterTool) mergeApplication(existing, update *config.Application) {
	if update.Name != "" {
		existing.Name = update.Name
	}
	if update.Description != "" {
		existing.Description = update.Description
	}
	if update.Path != "" {
		existing.Path = update.Path
	}
	if update.Language != "" {
		existing.Language = update.Language
	}
	if update.Type != "" {
		existing.Type = update.Type
	}
	if len(update.Build) > 0 {
		existing.Build = update.Build
	}
	if len(update.Test) > 0 {
		existing.Test = update.Test
	}
	if len(update.Lint) > 0 {
		existing.Lint = update.Lint
	}
	if len(update.KeyFiles) > 0 {
		existing.KeyFiles = update.KeyFiles
	}
	if update.Metadata != nil {
		if existing.Metadata == nil {
			existing.Metadata = make(map[string]interface{})
		}
		for k, v := range update.Metadata {
			existing.Metadata[k] = v
		}
	}
}
