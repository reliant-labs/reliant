// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"io/fs"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// BuiltinLayout lays out the builtin workflows the binary embeds, with their
// <slug>/scenarios/ directories, by the rule a project's workflows directory
// follows. `reliant workflow scenario run --include-builtins` and the builtin
// scenario tests both read the builtins through it, so the CLI runs exactly
// the scenarios go test does.
func BuiltinLayout() (*Layout, error) {
	files := map[string][]byte{}
	for _, fsys := range []fs.FS{builtin.BuiltinWorkflowsFS, builtin.BuiltinScenarioDirsFS} {
		err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(fsys, p)
			files[p] = data
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return NewLayout(files), nil
}
