package testretry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

const maxSource = 4 << 20

// git performs object reads only. Strip repository/environment indirection
// from its inherited settings and never run hooks, fsmonitor or lazy fetch.
func git(ctx context.Context, root, gitExe string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, gitExe, append([]string{"--no-replace-objects", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-C", root}, args...)...)
	command.Env = append(slices.DeleteFunc(os.Environ(), func(key string) bool { return strings.HasPrefix(key, "GIT_") }),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	return command.Output()
}

func treeState(ctx context.Context, cfg Config, gitExe string) (string, error) {
	head, err := git(ctx, cfg.Root, gitExe, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != cfg.Head {
		return "", errors.New("tested HEAD does not match the gate")
	}
	base, err := git(ctx, cfg.Root, gitExe, "merge-base", cfg.Base, cfg.Head)
	if err != nil || strings.TrimSpace(string(base)) != cfg.Base {
		return "", errors.New("gate base is not an ancestor")
	}
	dirty, err := git(ctx, cfg.Root, gitExe, "status", "--porcelain=v1", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil || len(dirty) != 0 {
		return "", errors.New("tested checkout changed or cannot be read")
	}
	tree, err := git(ctx, cfg.Root, gitExe, "rev-parse", cfg.Head+"^{tree}")
	if result := strings.TrimSpace(string(tree)); err == nil && shaPattern.MatchString(result) {
		return result, nil
	}
	return "", errors.New("tested tree could not be resolved")
}

type sourceRef struct{ file, blob string }

// A gitignored test file can affect go test without making git status dirty.
// Reject it rather than try to verify source or TestMain from untracked input.
func trackedTestInputs(ctx context.Context, cfg Config, gitExe, module, pkg string) error {
	dir := strings.TrimPrefix(strings.TrimPrefix(pkg, module), "/")
	if dir != "" {
		dir += "/"
	}
	untracked, err := git(ctx, cfg.Root, gitExe, "ls-files", "-z", "--others", "--", ":(glob)"+dir+"*_test.go")
	if err != nil || len(untracked) != 0 {
		return errors.New("package test source is untracked or unreadable")
	}
	return nil
}

func locate(ctx context.Context, cfg Config, gitExe, module, revision string, key identity) (sourceRef, error) {
	if !shaPattern.MatchString(revision) || !packagePattern.MatchString(module) || !testPattern.MatchString(key.name) {
		return sourceRef{}, errors.New("invalid revision, module, or test identity")
	}
	if packages, err := packageSet(module, []string{key.pkg}); err != nil || !packages[key.pkg] {
		return sourceRef{}, errors.New("invalid test package identity")
	}
	dir := strings.TrimPrefix(strings.TrimPrefix(key.pkg, module), "/")
	// Read the package's tree as an object, not a pathspec against the
	// revision root. ls-tree is non-recursive here deliberately: a child
	// package may declare the same test, but is not part of this package.
	tree := revision
	if dir != "" {
		tree += ":" + dir
	}
	entries, err := git(ctx, cfg.Root, gitExe, "ls-tree", "-z", tree)
	if err != nil {
		return sourceRef{}, fmt.Errorf("list test source: %w", err)
	}
	var found sourceRef
	for row := range bytes.SplitSeq(entries, []byte{0}) {
		metadata, filename, ok := bytes.Cut(row, []byte{'\t'})
		file := string(filename)
		if !ok || strings.Contains(file, "/") || !strings.HasSuffix(file, "_test.go") {
			continue
		}
		if dir != "" {
			file = dir + "/" + file
		}
		fields := strings.Fields(string(metadata))
		if len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || !shaPattern.MatchString(fields[2]) {
			return sourceRef{}, errors.New("non-regular Go test source")
		}
		blob := fields[2]
		data, err := git(ctx, cfg.Root, gitExe, "cat-file", "blob", blob)
		if err != nil || len(data) > maxSource {
			return sourceRef{}, errors.New("cannot read test source blob")
		}
		// A custom TestMain may branch on -run even when the resulting
		// single test has the same name. Look across both internal and
		// external test files, including those excluded by build tags.
		if !bytes.Contains(data, []byte(key.name)) && !bytes.Contains(data, []byte("TestMain")) {
			continue
		}
		decl, err := testDeclaration(data, key.name)
		if err != nil {
			return sourceRef{}, err
		}
		if decl == nil {
			continue
		}
		if found.file != "" {
			return sourceRef{}, errors.New("test declaration is ambiguous across package files")
		}
		found = sourceRef{file, blob}
	}
	if found.file == "" {
		return sourceRef{}, errors.New("no tracked top-level Go test matches the failure")
	}
	return found, nil
}

// testDeclaration never trusts a string/line match as a test declaration.
func testDeclaration(source []byte, name string) (*ast.FuncDecl, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "tracked_test.go", source, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("cannot parse tracked test: %w", err)
	}
	var found *ast.FuncDecl
	for _, candidate := range parsed.Decls {
		fn, ok := candidate.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "TestMain" {
			return nil, errors.New("custom TestMain can change the original test setup")
		}
		if fn.Name.Name != name {
			continue
		}
		if found != nil || ast.IsGenerated(parsed) || !isGoTest(parsed, fn) {
			return nil, errors.New("ambiguous, invalid, or generated Go test source")
		}
		found = fn
	}
	return found, nil
}

func isGoTest(file *ast.File, fn *ast.FuncDecl) bool {
	if fn.Body == nil || fn.Type.TypeParams != nil || fn.Type.Results != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := star.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "T" {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || importPath != "testing" {
			continue
		}
		alias := "testing"
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		return alias == qualifier.Name
	}
	return false
}
