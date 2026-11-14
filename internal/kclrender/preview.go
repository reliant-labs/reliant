package kclrender

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreviewMarkerFile is the file `forge env preview create` writes into a
// preview instance's env directory (deploy/kcl/<instance>/). Its presence is
// what makes the directory a preview instance; its fields are bound on every
// render of it as `-D forge_preview_*`, which Bundle.preview defaults from.
//
// WHY A MARKER AND NOT AN EDIT TO main.k. The instance's KCL is a verbatim
// copy of its template env's, so nothing about a preview is spliced into a
// file the user wrote, and every render path — `env render`, `env deploy`,
// `env diff`, the cluster apply — sees the same preview because they all
// render through Run.
const PreviewMarkerFile = ".forge-preview.json"

// PreviewMarker is the marker's content.
type PreviewMarker struct {
	Of        string `json:"of"`
	ExpiresAt string `json:"expires_at"`
	Source    string `json:"source,omitempty"`
}

// ReadPreviewMarker reads the marker of the env directory dir. ok is false
// when there is none.
func ReadPreviewMarker(dir string) (m PreviewMarker, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, PreviewMarkerFile))
	if os.IsNotExist(err) {
		return PreviewMarker{}, false, nil
	}
	if err != nil {
		return PreviewMarker{}, false, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return PreviewMarker{}, false, fmt.Errorf("%s: %w", filepath.Join(dir, PreviewMarkerFile), err)
	}
	return m, true, nil
}

// withPreviewDArgs binds the preview marker of the env directory source (or
// of the directory a single source file is in), when there is one. A key the
// caller bound wins. A marker that cannot be read binds nothing: the render
// then has no preview, and the KCL that requires one says so.
func withPreviewDArgs(source string, dArgs []string) []string {
	dir := source
	if fi, err := os.Stat(source); err == nil && !fi.IsDir() {
		dir = filepath.Dir(source)
	}
	m, ok, err := ReadPreviewMarker(dir)
	if err != nil || !ok || m.Of == "" {
		return dArgs
	}
	add := []string{
		fmt.Sprintf("forge_preview_of=%q", m.Of),
		fmt.Sprintf("forge_preview_expires_at=%q", m.ExpiresAt),
	}
	if m.Source != "" {
		add = append(add, fmt.Sprintf("forge_preview_source=%q", m.Source))
	}
	out := append([]string(nil), dArgs...)
	for _, a := range add {
		key, _, _ := strings.Cut(a, "=")
		bound := false
		for _, existing := range dArgs {
			if strings.HasPrefix(existing, key+"=") {
				bound = true
				break
			}
		}
		if !bound {
			out = append(out, a)
		}
	}
	return out
}
