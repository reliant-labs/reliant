// Copyright (c) 2025 Reliant Labs
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// ProjectMeta represents the complete project metadata structure
type ProjectMeta struct {
	Version      string        `yaml:"version"`
	GeneratedAt  time.Time     `yaml:"generated_at"`
	Project      ProjectInfo   `yaml:"project"`
	Applications []Application `yaml:"applications,omitempty"`
}

// ProjectInfo contains project-level information
type ProjectInfo struct {
	Name   string       `yaml:"name"`
	Type   ProjectType  `yaml:"type"` // monorepo or single
	Root   string       `yaml:"root"`
	Global GlobalConfig `yaml:"global,omitempty"`
}

// ProjectType represents the project structure type
type ProjectType string

const (
	ProjectTypeMonorepo ProjectType = "monorepo"
	ProjectTypeSingle   ProjectType = "single"
)

// GlobalConfig contains project-wide configurations
type GlobalConfig struct {
	Tests    []CommandConfig        `yaml:"tests,omitempty"`
	Linters  []LinterConfig         `yaml:"linters,omitempty"`
	Metadata map[string]interface{} `yaml:"metadata,omitempty"`
}

// Application represents a single application/service in the project
type Application struct {
	ID          string                 `yaml:"id"`
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description,omitempty"`
	Path        string                 `yaml:"path"`
	Language    string                 `yaml:"language"`
	Type        ApplicationType        `yaml:"type"`
	Build       []BuildConfig          `yaml:"build,omitempty"`
	Test        []CommandConfig        `yaml:"test,omitempty"`
	Lint        []LinterConfig         `yaml:"lint,omitempty"`
	KeyFiles    []KeyFile              `yaml:"key_files,omitempty"`
	Metadata    map[string]interface{} `yaml:"metadata,omitempty"`
}

// ApplicationType represents the type of application
type ApplicationType string

const (
	ApplicationTypeBinary  ApplicationType = "binary"
	ApplicationTypeLibrary ApplicationType = "library"
	ApplicationTypeService ApplicationType = "service"
	ApplicationTypeWebapp  ApplicationType = "webapp"
)

// BuildConfig represents a build configuration
type BuildConfig struct {
	Name        string   `yaml:"name"`
	Command     string   `yaml:"command"`
	Env         []string `yaml:"env,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

// CommandConfig represents a generic command configuration
type CommandConfig struct {
	Name        string `yaml:"name"`
	Command     string `yaml:"command"`
	Description string `yaml:"description,omitempty"`
}

// LinterConfig represents a linter configuration
type LinterConfig struct {
	Name        string `yaml:"name"`
	Command     string `yaml:"command"`
	Description string `yaml:"description,omitempty"`
}

// KeyFile represents an important file in the project
type KeyFile struct {
	Path        string `yaml:"path"`
	Description string `yaml:"description"`
}

// LoadProjectMeta loads the project metadata from the default location
func LoadProjectMeta() (*ProjectMeta, error) {
	metaPath := filepath.Join(".reliant", "project-meta.yaml")
	return LoadProjectMetaFrom(metaPath)
}

// LoadProjectMetaFrom loads project metadata from a specific path
func LoadProjectMetaFrom(path string) (*ProjectMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project metadata not found at %s", path)
		}
		return nil, fmt.Errorf("failed to read project metadata: %w", err)
	}

	var meta ProjectMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse project metadata: %w", err)
	}

	return &meta, nil
}

// SaveProjectMeta saves the project metadata to the default location
func SaveProjectMeta(meta *ProjectMeta) error {
	metaPath := filepath.Join(".reliant", "project-meta.yaml")
	return SaveProjectMetaTo(meta, metaPath)
}

// SaveProjectMetaTo saves project metadata to a specific path
func SaveProjectMetaTo(meta *ProjectMeta, path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Set version and timestamp
	if meta.Version == "" {
		meta.Version = "1.0"
	}
	meta.GeneratedAt = time.Now()

	data, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal project metadata: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write project metadata: %w", err)
	}

	return nil
}

// Validate checks if the project metadata is valid
func (m *ProjectMeta) Validate() error {
	if m.Version == "" {
		return fmt.Errorf("version is required")
	}

	if m.Project.Name == "" {
		return fmt.Errorf("project name is required")
	}

	if m.Project.Type != ProjectTypeMonorepo && m.Project.Type != ProjectTypeSingle {
		return fmt.Errorf("invalid project type: %s", m.Project.Type)
	}

	if m.Project.Root == "" {
		return fmt.Errorf("project root is required")
	}

	// Validate applications
	appIDs := make(map[string]bool)
	for _, app := range m.Applications {
		if app.ID == "" {
			return fmt.Errorf("application ID is required")
		}
		if appIDs[app.ID] {
			return fmt.Errorf("duplicate application ID: %s", app.ID)
		}
		appIDs[app.ID] = true

		if app.Name == "" {
			return fmt.Errorf("application name is required for %s", app.ID)
		}
		if app.Path == "" {
			return fmt.Errorf("application path is required for %s", app.ID)
		}
	}

	return nil
}

// FindApplication finds an application by ID
func (m *ProjectMeta) FindApplication(id string) (*Application, error) {
	for i := range m.Applications {
		if m.Applications[i].ID == id {
			return &m.Applications[i], nil
		}
	}
	return nil, fmt.Errorf("application not found: %s", id)
}

// GetBuildCommand gets a build command for an application
func (a *Application) GetBuildCommand(name string) (*BuildConfig, error) {
	for i := range a.Build {
		if a.Build[i].Name == name {
			return &a.Build[i], nil
		}
	}
	return nil, fmt.Errorf("build configuration not found: %s", name)
}

// GetTestCommand gets a test command for an application
func (a *Application) GetTestCommand(name string) (*CommandConfig, error) {
	for i := range a.Test {
		if a.Test[i].Name == name {
			return &a.Test[i], nil
		}
	}
	return nil, fmt.Errorf("test configuration not found: %s", name)
}
