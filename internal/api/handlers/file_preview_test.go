package handlers

import "testing"

func TestFilePreviewHandlerRoutePrefix(t *testing.T) {
	h := &FilePreviewHandler{}

	if got, want := h.RoutePrefix(), "/files"; got != want {
		t.Fatalf("RoutePrefix() = %q, want %q", got, want)
	}
}
