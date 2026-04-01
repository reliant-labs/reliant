// Copyright (c) 2025 Reliant Labs
package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProjectMeta_Validate(t *testing.T) {
	tests := []struct {
		name    string
		meta    ProjectMeta
		wantErr bool
	}{
		{
			name: "valid metadata",
			meta: ProjectMeta{
				Version:     "1.0",
				GeneratedAt: time.Now(),
				Project: ProjectInfo{
					Name: "test-project",
					Type: ProjectTypeSingle,
					Root: "/test",
				},
				Applications: []Application{
					{
						ID:       "app1",
						Name:     "Test App",
						Path:     "./app1",
						Language: "go",
						Type:     ApplicationTypeBinary,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing version",
			meta: ProjectMeta{
				Project: ProjectInfo{
					Name: "test-project",
					Type: ProjectTypeSingle,
					Root: "/test",
				},
			},
			wantErr: true,
		},
		{
			name: "missing project name",
			meta: ProjectMeta{
				Version: "1.0",
				Project: ProjectInfo{
					Type: ProjectTypeSingle,
					Root: "/test",
				},
			},
			wantErr: true,
		},
		{
			name: "duplicate application IDs",
			meta: ProjectMeta{
				Version: "1.0",
				Project: ProjectInfo{
					Name: "test-project",
					Type: ProjectTypeMonorepo,
					Root: "/test",
				},
				Applications: []Application{
					{ID: "app1", Name: "App 1", Path: "./app1"},
					{ID: "app1", Name: "App 2", Path: "./app2"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.meta.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("ProjectMeta.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestProjectMeta_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	metaPath := filepath.Join(tmpDir, "test-meta.yaml")

	original := &ProjectMeta{
		Version:     "1.0",
		GeneratedAt: time.Now().Truncate(time.Second), // Truncate for comparison
		Project: ProjectInfo{
			Name: "test-project",
			Type: ProjectTypeSingle,
			Root: "/test",
			Global: GlobalConfig{
				Tests: []CommandConfig{
					{Name: "unit", Command: "go test ./..."},
				},
				Linters: []LinterConfig{
					{Name: "golangci-lint", Command: "golangci-lint run"},
				},
			},
		},
		Applications: []Application{
			{
				ID:       "app1",
				Name:     "Test App",
				Path:     "./app1",
				Language: "go",
				Type:     ApplicationTypeBinary,
				Build: []BuildConfig{
					{Name: "production", Command: "go build -o bin/app1 ./app1"},
				},
				Test: []CommandConfig{
					{Name: "unit", Command: "go test ./app1/..."},
				},
				KeyFiles: []KeyFile{
					{Path: "app1/main.go", Description: "Entry point"},
				},
			},
		},
	}

	// Test save
	err := SaveProjectMetaTo(original, metaPath)
	if err != nil {
		t.Fatalf("SaveProjectMetaTo() error = %v", err)
	}

	// Test load
	loaded, err := LoadProjectMetaFrom(metaPath)
	if err != nil {
		t.Fatalf("LoadProjectMetaFrom() error = %v", err)
	}

	// Basic structure comparison
	if loaded.Version != original.Version {
		t.Errorf("Version mismatch: got %v, want %v", loaded.Version, original.Version)
	}
	if loaded.Project.Name != original.Project.Name {
		t.Errorf("Project name mismatch: got %v, want %v", loaded.Project.Name, original.Project.Name)
	}
	if len(loaded.Applications) != len(original.Applications) {
		t.Errorf("Applications count mismatch: got %v, want %v", len(loaded.Applications), len(original.Applications))
	}
}

func TestProjectMeta_FindApplication(t *testing.T) {
	meta := &ProjectMeta{
		Applications: []Application{
			{ID: "app1", Name: "App 1"},
			{ID: "app2", Name: "App 2"},
		},
	}

	// Test finding existing application
	app, err := meta.FindApplication("app1")
	if err != nil {
		t.Errorf("FindApplication() error = %v", err)
	}
	if app.Name != "App 1" {
		t.Errorf("Wrong application found: got %v, want %v", app.Name, "App 1")
	}

	// Test finding non-existent application
	_, err = meta.FindApplication("app3")
	if err == nil {
		t.Error("FindApplication() expected error for non-existent app")
	}
}

func TestApplication_GetBuildCommand(t *testing.T) {
	app := &Application{
		Build: []BuildConfig{
			{Name: "dev", Command: "go build -o dev ./..."},
			{Name: "prod", Command: "go build -ldflags='-s -w' -o prod ./..."},
		},
	}

	// Test finding existing command
	cmd, err := app.GetBuildCommand("prod")
	if err != nil {
		t.Errorf("GetBuildCommand() error = %v", err)
	}
	if cmd.Command != "go build -ldflags='-s -w' -o prod ./..." {
		t.Errorf("Wrong command: got %v", cmd.Command)
	}

	// Test finding non-existent command
	_, err = app.GetBuildCommand("test")
	if err == nil {
		t.Error("GetBuildCommand() expected error for non-existent command")
	}
}
