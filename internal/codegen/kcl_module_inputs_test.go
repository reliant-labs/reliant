package codegen

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// forgeKCLModuleDir is the on-disk kcl/ module a child `kcl run` resolves as
// its `forge` dependency, after every file in it has been read by THIS
// process.
//
// The read is what keeps `go test`'s cache honest. The cache keys a result on
// the files the test process itself opens or stats; a child `kcl` reading
// kcl/*.k is another process, so without this a cached `ok` survives an edit
// to the module that breaks the render. Reading the tree here makes those
// files test inputs.
func forgeKCLModuleDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(forgeRepoRoot(t), "kcl")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		_, err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatalf("read forge's KCL module %s as test input: %v", dir, err)
	}
	return dir
}
