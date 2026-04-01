package filepreview

import (
	"mime"
	"path/filepath"
	"strings"
)

// ViewerKind defines how a file should be rendered in the Reliant viewer.
type ViewerKind string

const (
	ViewerKindUnspecified ViewerKind = ""
	ViewerKindText        ViewerKind = "text"
	ViewerKindImage       ViewerKind = "image"
	ViewerKindPDF         ViewerKind = "pdf"
	ViewerKindAudio       ViewerKind = "audio"
	ViewerKindVideo       ViewerKind = "video"
	ViewerKindBinary      ViewerKind = "binary"
)

// Classification is the canonical preview contract used across backend callers.
type Classification struct {
	ViewerKind ViewerKind
	MIMEType   string
	IsBinary   bool
	IsEditable bool
}

var imageExtensions = map[string]string{
	".jpg":  "image/jpeg",
	".jpe":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".bmp":  "image/bmp",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".webp": "image/webp",
	".avif": "image/avif",
	".svg":  "image/svg+xml",
}

var audioExtensions = map[string]string{
	".mp3": "audio/mpeg",
	".wav": "audio/wav",
	".ogg": "audio/ogg",
	".oga": "audio/ogg",
}

var videoExtensions = map[string]string{
	".mp4":  "video/mp4",
	".webm": "video/webm",
}

var binaryExtensions = map[string]string{
	// Databases
	".db":      "application/octet-stream",
	".sqlite":  "application/octet-stream",
	".sqlite3": "application/octet-stream",
	".db-shm":  "application/octet-stream",
	".db-wal":  "application/octet-stream",
	".lock":    "text/plain",
	".sum":     "text/plain",

	// Executables and native artifacts
	".exe":   "application/vnd.microsoft.portable-executable",
	".dll":   "application/octet-stream",
	".so":    "application/octet-stream",
	".dylib": "application/octet-stream",
	".bin":   "application/octet-stream",
	".dat":   "application/octet-stream",
	".class": "application/java-vm",
	".pyc":   "application/octet-stream",
	".pyo":   "application/octet-stream",
	".o":     "application/octet-stream",
	".a":     "application/octet-stream",
	".lib":   "application/octet-stream",

	// Documents that should not be treated as text in the viewer
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",

	// Archives
	".zip": "application/zip",
	".tar": "application/x-tar",
	".gz":  "application/gzip",
	".bz2": "application/x-bzip2",
	".7z":  "application/x-7z-compressed",
	".rar": "application/vnd.rar",

	// Fonts
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".eot":   "application/vnd.ms-fontobject",
}

var textFilenames = map[string]bool{
	"dockerfile":     true,
	"makefile":       true,
	"cmakelists.txt": true,
	"gemfile":        true,
	"rakefile":       true,
	"vagrantfile":    true,
	"procfile":       true,
	"brewfile":       true,
	".gitignore":     true,
	".gitattributes": true,
	".dockerignore":  true,
	".editorconfig":  true,
	".prettierrc":    true,
	".eslintrc":      true,
	".babelrc":       true,
	".npmrc":         true,
	".nvmrc":         true,
	".yarnrc":        true,
	"license":        true,
	"readme":         true,
	"changelog":      true,
	"authors":        true,
	"contributors":   true,
	"copying":        true,
	"notice":         true,
	"version":        true,
}

// Classify returns the canonical preview classification for a file path and optional content sample.
// The sample is only used for fallback binary detection when the extension is unknown.
func Classify(filePath string, sample []byte) Classification {
	basename := strings.ToLower(filepath.Base(filePath))
	if textFilenames[basename] {
		return Classification{
			ViewerKind: ViewerKindText,
			MIMEType:   "text/plain",
			IsBinary:   false,
			IsEditable: true,
		}
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	if mimeType, ok := imageExtensions[ext]; ok {
		return Classification{
			ViewerKind: ViewerKindImage,
			MIMEType:   mimeType,
			IsBinary:   ext != ".svg",
			IsEditable: false,
		}
	}
	if ext == ".pdf" {
		return Classification{
			ViewerKind: ViewerKindPDF,
			MIMEType:   "application/pdf",
			IsBinary:   true,
			IsEditable: false,
		}
	}
	if mimeType, ok := audioExtensions[ext]; ok {
		return Classification{
			ViewerKind: ViewerKindAudio,
			MIMEType:   mimeType,
			IsBinary:   true,
			IsEditable: false,
		}
	}
	if mimeType, ok := videoExtensions[ext]; ok {
		return Classification{
			ViewerKind: ViewerKindVideo,
			MIMEType:   mimeType,
			IsBinary:   true,
			IsEditable: false,
		}
	}
	if mimeType, ok := binaryExtensions[ext]; ok {
		return Classification{
			ViewerKind: ViewerKindBinary,
			MIMEType:   mimeType,
			IsBinary:   true,
			IsEditable: false,
		}
	}
	if len(sample) > 0 && HasBinaryContent(sample) {
		return Classification{
			ViewerKind: ViewerKindBinary,
			MIMEType:   "application/octet-stream",
			IsBinary:   true,
			IsEditable: false,
		}
	}

	mimeType := mimeTypeFromExtension(ext)
	return Classification{
		ViewerKind: ViewerKindText,
		MIMEType:   mimeType,
		IsBinary:   false,
		IsEditable: true,
	}
}

// IsBinaryExtension reports whether the extension is known to be non-text for search/edit flows.
func IsBinaryExtension(ext string) bool {
	ext = strings.ToLower(ext)
	if ext == ".svg" {
		return false
	}
	if ext == ".pdf" {
		return true
	}
	if _, ok := imageExtensions[ext]; ok {
		return true
	}
	if _, ok := audioExtensions[ext]; ok {
		return true
	}
	if _, ok := videoExtensions[ext]; ok {
		return true
	}
	_, ok := binaryExtensions[ext]
	return ok
}

// HasBinaryContent reports whether a content sample looks binary.
func HasBinaryContent(content []byte) bool {
	sampleSize := 8192
	if len(content) < sampleSize {
		sampleSize = len(content)
	}
	for i := 0; i < sampleSize; i++ {
		if content[i] == 0 {
			return true
		}
	}
	return false
}

func mimeTypeFromExtension(ext string) string {
	if ext != "" {
		if contentType := mime.TypeByExtension(ext); contentType != "" {
			if idx := strings.Index(contentType, ";"); idx >= 0 {
				return contentType[:idx]
			}
			return contentType
		}
	}
	return "text/plain"
}
