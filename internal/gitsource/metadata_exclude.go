package gitsource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// excludeCacheMetadata keeps resolver-owned state out of git status, including
// entries fetched by older forge builds. Only cache checkouts call this; local
// overrides belong to the user. Fake fetchers need not create a git checkout.
func excludeCacheMetadata(entry string) error {
	gitDir := filepath.Join(entry, ".git")
	if info, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("source cache %s: .git is not a directory", entry)
	}
	file := filepath.Join(gitDir, "info", "exclude")
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "/"+MetadataFile {
			return nil
		}
	}
	data = append(data, []byte("\n/"+MetadataFile+"\n")...)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(file, data, 0o644)
}
