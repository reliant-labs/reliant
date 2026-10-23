package forgeconv

import (
	"os"
	"path/filepath"
)

// mkdirAllImpl is split from forgeconv_test.go so the test helpers don't
// pull os/filepath into the main test surface; this also lets us swap to
// mocked impls if the linter gets a Filesystem abstraction later.
func mkdirAllImpl(dir string) error {
	return os.MkdirAll(filepath.Clean(dir), 0o755)
}

func writeFileImpl(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
