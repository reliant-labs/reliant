// Copyright (c) 2025 Reliant Labs
package attachment

import "testing"

func TestGetAttachmentType(t *testing.T) {
	cases := []struct {
		filename string
		want     AttachmentType
	}{
		{"photo.png", TypeImage},
		{"photo.JPG", TypeImage},
		{"clip.mp4", TypeVideo},
		{"CLIP.MOV", TypeVideo},
		{"clip.webm", TypeVideo},
		{"report.pdf", TypeDocument},
		{"REPORT.PDF", TypeDocument},
		{"notes.docx", TypeFileReference},
		{"readme.md", TypeFileReference},
		{"main.go", TypeFileReference},
		{"Dockerfile", TypeFileReference},
		{"archive.zip", TypeUnsupported},
		{"noext", TypeUnsupported},
	}
	for _, c := range cases {
		if got := GetAttachmentType(c.filename); got != c.want {
			t.Errorf("GetAttachmentType(%q) = %q, want %q", c.filename, got, c.want)
		}
	}
}

// TestPDFIsDocumentNotFileReference guards the core fix: PDFs must route to the
// native-document path (read on demand via read_attachment), not the text
// extractor that previously failed on real PDFs.
func TestPDFIsDocumentNotFileReference(t *testing.T) {
	if got := GetAttachmentType("x.pdf"); got != TypeDocument {
		t.Fatalf("PDF must be TypeDocument, got %q", got)
	}
	if !GetAttachmentType("x.pdf").IsDocument() {
		t.Fatal("IsDocument() should be true for a PDF")
	}
}

func TestVideoMimeTypes(t *testing.T) {
	for ext, want := range map[string]string{".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime"} {
		if got := GetMimeType(ext); got != want {
			t.Errorf("GetMimeType(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestCheckGeneratedArtifactSize(t *testing.T) {
	if err := CheckGeneratedArtifactSize(1); err != nil {
		t.Errorf("1 byte: %v", err)
	}
	if err := CheckGeneratedArtifactSize(MaxGeneratedArtifactBytes); err != nil {
		t.Errorf("at cap: %v", err)
	}
	if err := CheckGeneratedArtifactSize(MaxGeneratedArtifactBytes + 1); err == nil {
		t.Error("over cap must fail")
	}
	if err := CheckGeneratedArtifactSize(0); err == nil {
		t.Error("empty must fail")
	}
}
