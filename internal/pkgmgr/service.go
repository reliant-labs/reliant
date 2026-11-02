// Copyright (c) 2025 Reliant Labs
package pkgmgr

import (
	"context"
	"path/filepath"
	"sort"
	"sync"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Service provides package management operations
//
// It only discovers commands; running one is the daemon's exec.bg_start
// (internal/handlers/package_commands), on the machine holding the workspace.
type Service struct {
	adapters   []Adapter
	adaptersMu sync.RWMutex
}

// NewService creates a new package manager service with default adapters
func NewService() *Service {
	return &Service{
		adapters: []Adapter{
			NewMakefileAdapter(),
			NewNPMAdapter(),
			NewTaskfileAdapter(),
		},
	}
}

// NewServiceWithAdapters creates a service with custom adapters
func NewServiceWithAdapters(adapters []Adapter) *Service {
	return &Service{adapters: adapters}
}

// ListCommands discovers all available commands in the given directory and subdirectories.
// It automatically walks the directory tree to find all package.json, Makefile, and Taskfile files,
// skipping common non-project directories like node_modules, .git, dist, etc.
func (s *Service) ListCommands(ctx context.Context, rootDir string) (*CommandListResponse, error) {
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()

	response := &CommandListResponse{
		Commands:      make(map[PackageType][]Command),
		DetectedTypes: []PackageType{},
	}

	// Discover all directories containing package files
	opts := DefaultDiscoveryOptions()
	dirs, err := DiscoverDirectories(ctx, rootDir, opts)
	if err != nil {
		logging.ErrorContext(ctx, "Error discovering directories",
			"root", rootDir,
			"error", err)
		// Fall back to just root directory
		dirs = []string{rootDir}
	}

	detectedTypesSet := make(map[PackageType]bool)

	// Search each directory
	for _, dir := range dirs {
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		default:
		}

		// Calculate relative path for display
		relativePath := ""
		if dir != rootDir {
			rel, err := filepath.Rel(rootDir, dir)
			if err == nil {
				relativePath = rel
			}
		}

		for _, adapter := range s.adapters {
			detected, err := adapter.Detect(ctx, dir)
			if err != nil {
				continue
			}

			if !detected {
				continue
			}

			detectedTypesSet[adapter.Type()] = true

			commands, err := adapter.Parse(ctx, dir)
			if err != nil {
				logging.ErrorContext(ctx, "Error parsing package file",
					"type", adapter.Type(),
					"dir", dir,
					"error", err)
				continue
			}

			// Set WorkingDir and RelativePath on each command
			for i := range commands {
				commands[i].WorkingDir = dir
				commands[i].RelativePath = relativePath
			}

			// Append to existing commands for this type
			response.Commands[adapter.Type()] = append(response.Commands[adapter.Type()], commands...)
		}
	}

	// Convert detected types set to slice
	for t := range detectedTypesSet {
		response.DetectedTypes = append(response.DetectedTypes, t)
	}

	// Sort commands: by relative path first, then by name
	for pkgType := range response.Commands {
		sort.Slice(response.Commands[pkgType], func(i, j int) bool {
			ci, cj := response.Commands[pkgType][i], response.Commands[pkgType][j]
			// Root commands (empty RelativePath) come first
			if ci.RelativePath != cj.RelativePath {
				if ci.RelativePath == "" {
					return true
				}
				if cj.RelativePath == "" {
					return false
				}
				return ci.RelativePath < cj.RelativePath
			}
			return ci.Name < cj.Name
		})
	}

	return response, nil
}

// DetectedPackageTypes returns which package types exist in a directory
func (s *Service) DetectedPackageTypes(ctx context.Context, dir string) ([]PackageType, error) {
	s.adaptersMu.RLock()
	defer s.adaptersMu.RUnlock()

	var types []PackageType
	for _, adapter := range s.adapters {
		detected, err := adapter.Detect(ctx, dir)
		if err != nil {
			continue
		}
		if detected {
			types = append(types, adapter.Type())
		}
	}
	return types, nil
}
