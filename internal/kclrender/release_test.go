package kclrender

import (
	"context"
	"reflect"
	"testing"
)

func TestReleaseDArgs(t *testing.T) {
	if got := ReleaseDArgs(context.Background()); got != nil {
		t.Fatalf("unbound render must emit no release binding, got %v", got)
	}
	if got := ReleaseDArgs(WithRelease(context.Background(), "")); got != nil {
		t.Fatalf("an empty release must stay unbound, got %v", got)
	}
	// Quoted, so an all-digit version stays a KCL str (cf. image_tag).
	want := []string{`release_version="20261010"`}
	if got := ReleaseDArgs(WithRelease(context.Background(), "20261010")); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReleaseDArgs = %v, want %v", got, want)
	}
}

// TestRelease pins the getter the frontend build reads FORGE_RELEASE from: the
// same value ReleaseDArgs binds into the render, so a frontend and the
// workloads beside it can never report different releases.
func TestRelease(t *testing.T) {
	if got := Release(context.Background()); got != "" {
		t.Fatalf("unbound ctx must report no release, got %q", got)
	}
	if got := Release(WithRelease(context.Background(), "20261011.184732-0b1d2c3e4f5a")); got != "20261011.184732-0b1d2c3e4f5a" {
		t.Fatalf("Release = %q, want the bound version", got)
	}
}
