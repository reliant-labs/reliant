package filepreview

import "testing"

func TestClassifyKnownPreviewTypes(t *testing.T) {
	tests := []struct {
		path       string
		kind       ViewerKind
		mimeType   string
		isBinary   bool
		isEditable bool
	}{
		{path: "photo.avif", kind: ViewerKindImage, mimeType: "image/avif", isBinary: true, isEditable: false},
		{path: "diagram.svg", kind: ViewerKindImage, mimeType: "image/svg+xml", isBinary: false, isEditable: false},
		{path: "paper.pdf", kind: ViewerKindPDF, mimeType: "application/pdf", isBinary: true, isEditable: false},
		{path: "sound.oga", kind: ViewerKindAudio, mimeType: "audio/ogg", isBinary: true, isEditable: false},
		{path: "clip.webm", kind: ViewerKindVideo, mimeType: "video/webm", isBinary: true, isEditable: false},
		{path: "archive.zip", kind: ViewerKindBinary, mimeType: "application/zip", isBinary: true, isEditable: false},
		{path: "main.go", kind: ViewerKindText, mimeType: "text/plain", isBinary: false, isEditable: true},
		{path: "Dockerfile", kind: ViewerKindText, mimeType: "text/plain", isBinary: false, isEditable: true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := Classify(tt.path, nil)
			if got.ViewerKind != tt.kind {
				t.Fatalf("ViewerKind = %q, want %q", got.ViewerKind, tt.kind)
			}
			if got.MIMEType != tt.mimeType {
				t.Fatalf("MIMEType = %q, want %q", got.MIMEType, tt.mimeType)
			}
			if got.IsBinary != tt.isBinary {
				t.Fatalf("IsBinary = %v, want %v", got.IsBinary, tt.isBinary)
			}
			if got.IsEditable != tt.isEditable {
				t.Fatalf("IsEditable = %v, want %v", got.IsEditable, tt.isEditable)
			}
		})
	}
}

func TestClassifyFallsBackToBinaryContent(t *testing.T) {
	got := Classify("mysteryfile", []byte{0x00, 0x01, 0x02})
	if got.ViewerKind != ViewerKindBinary {
		t.Fatalf("ViewerKind = %q, want %q", got.ViewerKind, ViewerKindBinary)
	}
	if !got.IsBinary {
		t.Fatal("expected IsBinary to be true")
	}
}

func TestIsBinaryExtension(t *testing.T) {
	if !IsBinaryExtension(".pdf") {
		t.Fatal("expected .pdf to be binary")
	}
	if !IsBinaryExtension(".png") {
		t.Fatal("expected .png to be binary")
	}
	if IsBinaryExtension(".svg") {
		t.Fatal("expected .svg to not be treated as binary extension")
	}
	if IsBinaryExtension(".ts") {
		t.Fatal("expected .ts to not be binary")
	}
}

func TestHasBinaryContent(t *testing.T) {
	if !HasBinaryContent([]byte("hello\x00world")) {
		t.Fatal("expected content with null byte to be binary")
	}
	if HasBinaryContent([]byte("plain text")) {
		t.Fatal("expected plain text to not be binary")
	}
}
