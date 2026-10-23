package codegen

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for the duration of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	fn()
	return buf.String()
}

// A project with no service protos has NOTHING for protoc-gen-forge to
// describe, so a missing gen/forge_descriptor.json is the correct and
// final state — not a step the user forgot. Telling them to "run 'forge
// generate' with protoc-gen-forge" is advice that cannot be acted on: the
// pipeline skips descriptor extraction whenever features.codegen is off
// or no proto declares a service.
//
// forge's own repo is exactly this shape (a CLI, zero Connect services),
// and the notice printed on nearly every command — twice per
// `forge project upgrade --check`, three times per `forge generate` —
// which is how a line labelled "Info" became the most frequent output of
// a healthy tree.
func TestLoadDescriptor_NoServiceProtos_IsSilent(t *testing.T) {
	dir := t.TempDir()

	out := captureLog(t, func() {
		if _, err := ParseServicesFromProtos("", dir); err != nil {
			t.Fatalf("ParseServicesFromProtos: %v", err)
		}
		if _, err := ParseEntityProtos(dir); err != nil {
			t.Fatalf("ParseEntityProtos: %v", err)
		}
	})

	if strings.Contains(out, "forge_descriptor.json not found") {
		t.Errorf("a project with no service protos must not be told to generate a descriptor; got:\n%s", out)
	}
}

// The converse: when the project DOES declare a service in a proto, a
// missing descriptor is genuine pending work and must still be reported.
func TestLoadDescriptor_WithServiceProto_StillNotifies(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "proto", "services", "tasks", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "syntax = \"proto3\";\npackage tasks.v1;\nservice TaskService {\n}\n"
	if err := os.WriteFile(filepath.Join(protoDir, "tasks.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureLog(t, func() {
		if _, err := ParseServicesFromProtos("", dir); err != nil {
			t.Fatalf("ParseServicesFromProtos: %v", err)
		}
	})

	if !strings.Contains(out, "forge_descriptor.json not found") {
		t.Errorf("a project that declares a service and has no descriptor must still be told; got:\n%s", out)
	}
}
