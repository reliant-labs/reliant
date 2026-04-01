package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/filepreview"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/permission"
)

// FilePreviewHandler serves authenticated raw file content for in-app previews.
type FilePreviewHandler struct {
	database   db.Repository
	permission *ProjectPermissionHelper
}

func NewFilePreviewHandler(database db.Repository) *FilePreviewHandler {
	return &FilePreviewHandler{
		database:   database,
		permission: NewProjectPermissionHelper(database),
	}
}

func (h *FilePreviewHandler) RoutePrefix() string {
	return "/files"
}

func (h *FilePreviewHandler) Routes() []Route {
	return []Route{
		{Path: "/preview", Method: http.MethodGet, Handler: h.GetPreviewContent, RequireAuth: true},
	}
}

func (h *FilePreviewHandler) Can(r *http.Request, action permission.Action, resourceID string) error {
	return h.permission.Can(r, action, resourceID)
}

func (h *FilePreviewHandler) GetPreviewContent(w http.ResponseWriter, r *http.Request) {
	projectID := GetQueryParam(r, "project_id")
	if projectID == "" {
		RespondError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !CheckPermissionAndRespond(w, r, h, permission.ActionView, projectID) {
		return
	}

	requestedPath := GetQueryParam(r, "path")
	if requestedPath == "" {
		RespondError(w, http.StatusBadRequest, "path is required")
		return
	}

	var worktreeID *string
	if value := strings.TrimSpace(GetQueryParam(r, "worktree_id")); value != "" {
		worktreeID = &value
	}
	var chatID *string
	if value := strings.TrimSpace(GetQueryParam(r, "chat_id")); value != "" {
		chatID = &value
	}

	basePath, err := filepreview.ResolveBasePath(r.Context(), h.database, projectID, worktreeID, chatID)
	if err != nil {
		RespondError(w, http.StatusNotFound, "Workspace not found")
		return
	}
	absPath, err := filepreview.ValidatePath(basePath, requestedPath)
	if err != nil {
		RespondError(w, http.StatusForbidden, "Access denied")
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			RespondError(w, http.StatusNotFound, "File not found")
			return
		}
		logging.Error("Failed to stat preview file", "error", err, "path", absPath)
		RespondError(w, http.StatusInternalServerError, "Failed to read file")
		return
	}
	if info.IsDir() {
		RespondError(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	sample, err := readPreviewSample(absPath)
	if err != nil {
		logging.Error("Failed to read preview sample", "error", err, "path", absPath)
		RespondError(w, http.StatusInternalServerError, "Failed to read file")
		return
	}
	classification := filepreview.Classify(absPath, sample)
	if classification.ViewerKind != filepreview.ViewerKindImage &&
		classification.ViewerKind != filepreview.ViewerKindPDF &&
		classification.ViewerKind != filepreview.ViewerKindAudio &&
		classification.ViewerKind != filepreview.ViewerKindVideo {
		RespondError(w, http.StatusUnsupportedMediaType, "File type is not previewable")
		return
	}

	file, err := os.Open(absPath)
	if err != nil {
		logging.Error("Failed to open preview file", "error", err, "path", absPath)
		RespondError(w, http.StatusInternalServerError, "Failed to open file")
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", classification.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(absPath)))
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	if classification.ViewerKind == filepreview.ViewerKindPDF {
		w.Header().Set("Content-Security-Policy", "default-src 'self' blob: data:; img-src 'self' blob: data:; media-src 'self' blob: data:; frame-ancestors 'self'; object-src 'self' blob:;")
	}

	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), file)
}

func readPreviewSample(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	buf := make([]byte, 8192)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}
