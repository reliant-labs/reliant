// Copyright (c) 2025 Reliant Labs
package cmdutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CommandFinder helps locate executables in the system
type CommandFinder struct {
	// CommonPaths are common installation paths for the command
	CommonPaths []string
	// CommandName is the name of the command to find
	CommandName string
}

// NewCommandFinder creates a new command finder with default paths
func NewCommandFinder(commandName string) *CommandFinder {
	return &CommandFinder{
		CommandName: commandName,
		CommonPaths: getDefaultPaths(commandName),
	}
}

// Find attempts to locate the command in the system
func (cf *CommandFinder) Find() (string, error) {
	// First try to find it in common locations
	for _, path := range cf.CommonPaths {
		fullPath := filepath.Join(path, cf.CommandName)
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath, nil
		}
	}

	// Try exec.LookPath which uses PATH
	if path, err := exec.LookPath(cf.CommandName); err == nil {
		return path, nil
	}

	// If not found, try to use shell to find it with full PATH
	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		if runtime.GOOS == "windows" {
			shellPath = "cmd"
		} else {
			shellPath = "/bin/bash"
		}
	}

	// Use shell to find command with full PATH
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "where", cf.CommandName)
	} else {
		cmd = exec.Command(shellPath, "-l", "-c", fmt.Sprintf("which %s", cf.CommandName))
	}

	output, err := cmd.Output()
	if err == nil {
		cmdPath := strings.TrimSpace(string(output))
		if cmdPath != "" && !strings.Contains(cmdPath, "not found") {
			// On Windows, 'where' might return multiple paths, take the first one
			if runtime.GOOS == "windows" {
				if lines := strings.Split(cmdPath, "\n"); len(lines) > 0 {
					cmdPath = strings.TrimSpace(lines[0])
				}
			}
			return cmdPath, nil
		}
	}

	return "", fmt.Errorf("%s not found in PATH or common locations", cf.CommandName)
}

// getDefaultPaths returns common installation paths based on the command and OS
func getDefaultPaths(commandName string) []string {
	paths := []string{}

	// Add command-specific paths
	switch commandName {
	case "rg", "ripgrep":
		paths = append(paths, getRipgrepPaths()...)
	case "gh":
		paths = append(paths, getGitHubCLIPaths()...)
	case "git":
		paths = append(paths, getGitPaths()...)
	default:
		// For unknown commands, just use general paths
		paths = append(paths, getGeneralPaths()...)
	}

	return paths
}

func getRipgrepPaths() []string {
	paths := []string{}

	// First, check if we're running in a bundled Electron environment
	// The backend binary is at: <resourcesPath>/server/<platform-arch>/reliant-backend[.exe]
	// The tools will be at: <resourcesPath>/tools/<platform-arch>/rg[.exe]
	if bundledPath := getBundledToolPath("rg"); bundledPath != "" {
		paths = append(paths, bundledPath)
	}

	// Then add system paths as fallback
	paths = append(paths,
		"/opt/homebrew/bin/rg",              // Homebrew on Apple Silicon
		"/usr/local/bin/rg",                 // Homebrew on Intel or manual install
		"/usr/bin/rg",                       // System package manager
		"/home/linuxbrew/.linuxbrew/bin/rg", // Linuxbrew
	)

	if runtime.GOOS == "windows" {
		paths = append(paths,
			`C:\Program Files\ripgrep\rg.exe`,
			`C:\Tools\ripgrep\rg.exe`,
		)
	}

	return paths
}

// getBundledToolPath returns the path to a bundled tool if running in an Electron app
func getBundledToolPath(toolName string) string {
	// Get the current executable path
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}

	// Resolve symlinks
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return ""
	}

	// The backend is at: <resourcesPath>/server/<platform-arch>/reliant-backend[.exe]
	// We need to go up 2 directories to get to <resourcesPath>
	exeDir := filepath.Dir(exePath)
	resourcesPath := filepath.Join(exeDir, "..", "..")

	// Determine platform-arch string
	var platformArch string
	switch runtime.GOOS {
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			platformArch = "win32-amd64"
		case "arm64":
			platformArch = "win32-arm64"
		}
	case "darwin":
		switch runtime.GOARCH {
		case "amd64":
			platformArch = "mac-x64"
		case "arm64":
			platformArch = "mac-arm64"
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			platformArch = "linux-amd64"
		case "arm64":
			platformArch = "linux-arm64"
		}
	}

	if platformArch == "" {
		return ""
	}

	// Add .exe extension for Windows tools
	if runtime.GOOS == "windows" {
		toolName = toolName + ".exe"
	}

	// Build the path to the bundled tool
	toolPath := filepath.Join(resourcesPath, "tools", platformArch, toolName)

	// Check if the tool exists
	if _, err := os.Stat(toolPath); err == nil {
		return toolPath
	}

	return ""
}

func getGitHubCLIPaths() []string {
	paths := []string{
		"/opt/homebrew/bin/gh",              // Homebrew on Apple Silicon
		"/usr/local/bin/gh",                 // Homebrew on Intel or manual install
		"/usr/bin/gh",                       // System package manager
		"/home/linuxbrew/.linuxbrew/bin/gh", // Linuxbrew
		"/snap/bin/gh",                      // Snap on Linux
	}

	if runtime.GOOS == "windows" {
		paths = append(paths,
			`C:\Program Files\GitHub CLI\gh.exe`,
			`C:\Program Files (x86)\GitHub CLI\gh.exe`,
			`C:\Tools\gh\gh.exe`,
		)
	}

	return paths
}

func getGitPaths() []string {
	paths := []string{
		"/usr/bin/git",
		"/usr/local/bin/git",
		"/opt/homebrew/bin/git",
		"/home/linuxbrew/.linuxbrew/bin/git",
	}

	if runtime.GOOS == "windows" {
		paths = append(paths,
			`C:\Program Files\Git\bin\git.exe`,
			`C:\Program Files (x86)\Git\bin\git.exe`,
		)
	}

	if runtime.GOOS == "darwin" {
		// Xcode command line tools
		paths = append(paths, "/usr/bin/git")
	}

	return paths
}

func getGeneralPaths() []string {
	paths := []string{
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/opt/homebrew/bin",
		"/home/linuxbrew/.linuxbrew/bin",
	}

	if runtime.GOOS == "windows" {
		paths = append(paths,
			`C:\Windows\System32`,
			`C:\Windows`,
			`C:\Program Files`,
			`C:\Program Files (x86)`,
		)
	}

	// Add user-specific paths
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
		)

		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			paths = append(paths,
				filepath.Join(home, ".cargo", "bin"),
				filepath.Join(home, "go", "bin"),
			)
		}
	}

	return paths
}

// FindRipgrep is a convenience function for finding ripgrep
func FindRipgrep() (string, error) {
	finder := NewCommandFinder("rg")
	return finder.Find()
}

// FindGitHubCLI is a convenience function for finding GitHub CLI
func FindGitHubCLI() (string, error) {
	finder := NewCommandFinder("gh")
	return finder.Find()
}

// FindGit is a convenience function for finding git
func FindGit() (string, error) {
	finder := NewCommandFinder("git")
	return finder.Find()
}
