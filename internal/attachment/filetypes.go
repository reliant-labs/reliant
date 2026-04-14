// Copyright (c) 2025 Reliant Labs
package attachment

import (
	"path/filepath"
	"strings"
)

// AttachmentType represents how an attachment should be handled
type AttachmentType string

const (
	// TypeImage - binary image files sent as image blocks to LLM
	TypeImage AttachmentType = "image"
	// TypeFileReference - text files stored as path, content read at send time
	TypeFileReference AttachmentType = "file_reference"
	// TypeDocument - binary document files (PDF, etc.) sent natively to LLMs
	TypeDocument AttachmentType = "document"
	// TypeUnsupported - files that cannot be attached
	TypeUnsupported AttachmentType = "unsupported"
)

// Image extensions supported by Claude API (base64 image blocks)
var ImageExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
}

// Text file extensions that can be read and sent as text content
var TextExtensions = map[string]bool{
	// Markdown and documentation
	".md":       true,
	".markdown": true,
	".mdx":      true,
	".txt":      true,
	".text":     true,
	".rst":      true,

	// Data formats
	".json":       true,
	".yaml":       true,
	".yml":        true,
	".toml":       true,
	".xml":        true,
	".csv":        true,
	".tsv":        true,
	".ini":        true,
	".conf":       true,
	".cfg":        true,
	".env":        true,
	".properties": true,
	".pdf":        true,
	".docx":       true,

	// Programming languages
	".go":    true,
	".py":    true,
	".js":    true,
	".jsx":   true,
	".ts":    true,
	".tsx":   true,
	".rs":    true,
	".java":  true,
	".kt":    true,
	".scala": true,
	".c":     true,
	".cpp":   true,
	".cc":    true,
	".cxx":   true,
	".h":     true,
	".hpp":   true,
	".hxx":   true,
	".cs":    true,
	".swift": true,
	".rb":    true,
	".php":   true,
	".pl":    true,
	".pm":    true,
	".lua":   true,
	".r":     true,
	".R":     true,
	".m":     true, // Objective-C or MATLAB
	".mm":    true, // Objective-C++
	".zig":   true,
	".nim":   true,
	".v":     true, // V lang
	".d":     true, // D lang
	".dart":  true,
	".ex":    true, // Elixir
	".exs":   true,
	".erl":   true, // Erlang
	".hrl":   true,
	".clj":   true, // Clojure
	".cljs":  true,
	".cljc":  true,
	".fs":    true, // F#
	".fsx":   true,
	".ml":    true, // OCaml
	".mli":   true,
	".hs":    true, // Haskell
	".lhs":   true,
	".elm":   true,
	".purs":  true, // PureScript
	".jl":    true, // Julia

	// Shell and scripts
	".sh":   true,
	".bash": true,
	".zsh":  true,
	".fish": true,
	".ps1":  true, // PowerShell
	".psm1": true,
	".bat":  true,
	".cmd":  true,

	// Web
	".html":   true,
	".htm":    true,
	".css":    true,
	".scss":   true,
	".sass":   true,
	".less":   true,
	".vue":    true,
	".svelte": true,
	".astro":  true,

	// Config and build
	".dockerfile": true,
	".makefile":   true,
	".cmake":      true,
	".gradle":     true,
	".sbt":        true,
	".cabal":      true,
	".gemspec":    true,
	".podspec":    true,

	// SQL and queries
	".sql":     true,
	".graphql": true,
	".gql":     true,

	// Other text formats
	".log":    true,
	".diff":   true,
	".patch":  true,
	".proto":  true,
	".thrift": true,
	".avsc":   true, // Avro schema
	".tf":     true, // Terraform
	".tfvars": true,
	".hcl":    true, // HashiCorp Configuration Language
}

// Special filenames that are text files regardless of extension
var TextFilenames = map[string]bool{
	"Dockerfile":     true,
	"Makefile":       true,
	"CMakeLists.txt": true,
	"Gemfile":        true,
	"Rakefile":       true,
	"Vagrantfile":    true,
	"Procfile":       true,
	"Brewfile":       true,
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
	"LICENSE":        true,
	"README":         true,
	"CHANGELOG":      true,
	"AUTHORS":        true,
	"CONTRIBUTORS":   true,
	"COPYING":        true,
	"NOTICE":         true,
	"VERSION":        true,
}

// GetAttachmentType determines how a file should be handled based on its name
func GetAttachmentType(filename string) AttachmentType {
	// Check special filenames first
	basename := filepath.Base(filename)
	if TextFilenames[basename] {
		return TypeFileReference
	}

	// Get extension (lowercase for comparison)
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return TypeUnsupported
	}

	// Check if it's an image
	if ImageExtensions[ext] {
		return TypeImage
	}

	// Check if it's a text file
	if TextExtensions[ext] {
		return TypeFileReference
	}

	return TypeUnsupported
}

// GetMimeType returns the MIME type for a file extension
func GetMimeType(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
	// Images
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"

	// Text and data
	case ".txt", ".text":
		return "text/plain"
	case ".md", ".markdown", ".mdx":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".yaml", ".yml":
		return "application/x-yaml"
	case ".csv":
		return "text/csv"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js", ".jsx":
		return "application/javascript"
	case ".ts", ".tsx":
		return "application/typescript"

	default:
		return "application/octet-stream"
	}
}

// IsImageType checks if the attachment type is an image
func (t AttachmentType) IsImage() bool {
	return t == TypeImage
}

// IsFileReference checks if the attachment type is a file reference
func (t AttachmentType) IsFileReference() bool {
	return t == TypeFileReference
}

func (t AttachmentType) IsDocument() bool {
	return t == TypeDocument
}

// IsSupported checks if the attachment type is supported
func (t AttachmentType) IsSupported() bool {
	return t != TypeUnsupported
}

// SupportedExtensions returns a human-readable list of supported extensions
func SupportedExtensions() string {
	var exts []string
	for ext := range ImageExtensions {
		exts = append(exts, ext)
	}
	for ext := range TextExtensions {
		exts = append(exts, ext)
	}
	return strings.Join(exts, ", ")
}
