// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"os"
	"path/filepath"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
