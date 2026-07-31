// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryChildWorkflowInitPassesParentThread pins the one field of
// ChildWorkflowInitOpts that a caller cannot omit without silently corrupting
// the thread forest.
//
// initChildWorkflow only forwards `parent_thread` when opts.ParentThread is
// non-empty, so an omitted field is not a compile error and not a runtime
// error — it is a thread row born with a NULL parent_thread_id. That thread
// then reads as a forest ROOT, which `workflow ps` now depends on for its
// state rollup (56f66602): a nested child that should roll up into its parent
// instead reports as a top-level execution of its own.
//
// inline_workflow_executor.go's nested-workflow branch was exactly that: the
// only one of seven call sites that omitted it, so every thread born from a
// nested inline workflow was a forest root.
//
// The guard is structural rather than behavioural on purpose. The bug is a
// MISSING struct field, and the class of bug is "the eighth call site forgets
// too" — so the set is derived from the call sites themselves (every
// `initChildWorkflow(ChildWorkflowInitOpts{...})` in this package), never from
// a hand-maintained list of files, and an empty set fails loudly.
func TestEveryChildWorkflowInitPassesParentThread(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	require.NoError(t, err)

	callSites := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "initChildWorkflow" {
					return true
				}
				require.Len(t, call.Args, 1, "initChildWorkflow takes one opts struct")
				lit, ok := call.Args[0].(*ast.CompositeLit)
				if !ok {
					// A pre-built opts variable can't be checked here. If that
					// shape ever appears, this guard has to grow to follow it
					// rather than silently skip it.
					t.Errorf("%s: initChildWorkflow called with a non-literal argument; "+
						"this guard can only see inline ChildWorkflowInitOpts literals",
						fset.Position(call.Pos()))
					return true
				}
				callSites++

				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "ParentThread" {
						return true
					}
				}
				t.Errorf("%s (%s): ChildWorkflowInitOpts omits ParentThread — the child thread "+
					"is created with a NULL parent_thread_id and becomes a forest root, so "+
					"`workflow ps` rolls its state up nowhere. Pass the thread whose "+
					"ExecutionContext produced this child (the receiver of ForChild).",
					fset.Position(lit.Pos()), filepath.Base(path))
				return true
			})
		}
	}

	require.NotZero(t, callSites,
		"found no initChildWorkflow(ChildWorkflowInitOpts{...}) call sites to check — the guard "+
			"has stopped guarding anything (renamed function, or the callers moved package)")
}

// nopLogger is a no-op implementation of log.Logger for testing.
type nopLogger struct{}

func (nopLogger) Debug(string, ...interface{}) {}
func (nopLogger) Info(string, ...interface{})  {}
func (nopLogger) Warn(string, ...interface{})  {}
func (nopLogger) Error(string, ...interface{}) {}

func TestResolveInjectAttachments(t *testing.T) {
	logger := nopLogger{}

	t.Run("nil inject config fields", func(t *testing.T) {
		ic := &reliantv1.InjectConfig{}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		assert.Empty(t, files)
	})

	t.Run("id source", func(t *testing.T) {
		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{Source: &reliantv1.InjectAttachment_Id{Id: "att-123"}},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Equal(t, []string{"att-123"}, ids)
		assert.Empty(t, files)
	})

	t.Run("path source", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "inject-test-*.txt")
		require.NoError(t, err)
		t.Cleanup(func() { os.Remove(tmpFile.Name()) })

		content := []byte("hello world")
		_, err = tmpFile.Write(content)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{Source: &reliantv1.InjectAttachment_Path{Path: tmpFile.Name()}},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		require.Len(t, files, 1)
		assert.Equal(t, filepath.Base(tmpFile.Name()), files[0].Filename)
		assert.Equal(t, "text/plain", files[0].MIMEType)
		assert.Equal(t, content, files[0].Data)
	})

	t.Run("path source with filename override", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "inject-test-*.dat")
		require.NoError(t, err)
		t.Cleanup(func() { os.Remove(tmpFile.Name()) })

		_, err = tmpFile.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{
					Source:   &reliantv1.InjectAttachment_Path{Path: tmpFile.Name()},
					Filename: "custom-name.json",
				},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		require.Len(t, files, 1)
		assert.Equal(t, "custom-name.json", files[0].Filename)
		// MIME type derived from overridden filename
		assert.Equal(t, "application/json", files[0].MIMEType)
		assert.Equal(t, []byte("data"), files[0].Data)
	})

	t.Run("path source with mime_type override", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "inject-test-*.txt")
		require.NoError(t, err)
		t.Cleanup(func() { os.Remove(tmpFile.Name()) })

		_, err = tmpFile.Write([]byte("xml content"))
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{
					Source:   &reliantv1.InjectAttachment_Path{Path: tmpFile.Name()},
					MimeType: "application/xml",
				},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		require.Len(t, files, 1)
		assert.Equal(t, "application/xml", files[0].MIMEType)
	})

	t.Run("data source", func(t *testing.T) {
		rawData := []byte("raw binary content")
		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{
					Source:   &reliantv1.InjectAttachment_Data{Data: rawData},
					Filename: "report.pdf",
				},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		require.Len(t, files, 1)
		assert.Equal(t, "report.pdf", files[0].Filename)
		assert.Equal(t, "application/pdf", files[0].MIMEType)
		assert.Equal(t, rawData, files[0].Data)
	})

	t.Run("data source with auto mime_type", func(t *testing.T) {
		rawData := []byte(`{"key":"value"}`)
		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{
					Source:   &reliantv1.InjectAttachment_Data{Data: rawData},
					Filename: "config.json",
				},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		require.Len(t, files, 1)
		assert.Equal(t, "config.json", files[0].Filename)
		assert.Equal(t, "application/json", files[0].MIMEType)
		assert.Equal(t, rawData, files[0].Data)
	})

	t.Run("mixed sources", func(t *testing.T) {
		tmpFile, err := os.CreateTemp("", "inject-test-*.png")
		require.NoError(t, err)
		t.Cleanup(func() { os.Remove(tmpFile.Name()) })

		pngData := []byte("fake png")
		_, err = tmpFile.Write(pngData)
		require.NoError(t, err)
		require.NoError(t, tmpFile.Close())

		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{Source: &reliantv1.InjectAttachment_Id{Id: "id-1"}},
				{Source: &reliantv1.InjectAttachment_Path{Path: tmpFile.Name()}},
				{Source: &reliantv1.InjectAttachment_Id{Id: "id-2"}},
				{
					Source:   &reliantv1.InjectAttachment_Data{Data: []byte("inline")},
					Filename: "inline.txt",
				},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Equal(t, []string{"id-1", "id-2"}, ids)
		require.Len(t, files, 2)
		// First file: from path
		assert.Equal(t, filepath.Base(tmpFile.Name()), files[0].Filename)
		assert.Equal(t, pngData, files[0].Data)
		// Second file: from data
		assert.Equal(t, "inline.txt", files[1].Filename)
		assert.Equal(t, []byte("inline"), files[1].Data)
	})

	t.Run("legacy attachments", func(t *testing.T) {
		ic := &reliantv1.InjectConfig{
			LegacyAttachments: &reliantv1.CelString{
				Value: &reliantv1.CelString_Literal{Literal: `["legacy-id-1","legacy-id-2"]`},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Equal(t, []string{"legacy-id-1", "legacy-id-2"}, ids)
		assert.Empty(t, files)
	})

	t.Run("legacy + new attachments", func(t *testing.T) {
		ic := &reliantv1.InjectConfig{
			LegacyAttachments: &reliantv1.CelString{
				Value: &reliantv1.CelString_Literal{Literal: `["legacy-1"]`},
			},
			Attachments: []*reliantv1.InjectAttachment{
				{Source: &reliantv1.InjectAttachment_Id{Id: "new-1"}},
				{Source: &reliantv1.InjectAttachment_Id{Id: "new-2"}},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		// Legacy IDs come first (processed before new attachments)
		assert.Equal(t, []string{"legacy-1", "new-1", "new-2"}, ids)
		assert.Empty(t, files)
	})

	t.Run("path source with nonexistent file", func(t *testing.T) {
		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{Source: &reliantv1.InjectAttachment_Path{Path: "/tmp/nonexistent-file-abc123.txt"}},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		assert.Empty(t, files)
	})

	t.Run("empty path id data are skipped", func(t *testing.T) {
		ic := &reliantv1.InjectConfig{
			Attachments: []*reliantv1.InjectAttachment{
				{Source: &reliantv1.InjectAttachment_Id{Id: ""}},
				{Source: &reliantv1.InjectAttachment_Path{Path: ""}},
				{Source: &reliantv1.InjectAttachment_Data{Data: nil}},
				{Source: &reliantv1.InjectAttachment_Data{Data: []byte{}}},
			},
		}
		ids, files := resolveInjectAttachments(ic, logger)
		assert.Empty(t, ids)
		assert.Empty(t, files)
	})
}
