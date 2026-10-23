package codegen

import (
	"strings"
	"testing"
)

// blockCollisionMessages is the shape that broke: TWO composed config blocks
// that each declare a leaf named `base_domain`. Nothing about that is a proto
// error — a block is its own namespace, `StaticSiteConfig.base_domain` and
// `SimpleBackendConfig.base_domain` are distinct fields, and the Go loader
// binds each to its own env var. It is only the KCL projection that flattens
// every block's leaves into ONE `schema AppConfig`, and it is that flattening
// that turns two legal fields into one illegal declaration.
func blockCollisionMessages() []ConfigMessage {
	return []ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080", ProtoFile: "config/v1/config.proto"},
			{Name: "static_site", GoName: "StaticSite", ProtoType: "message", MessageType: "StaticSiteConfig", ProtoFile: "config/v1/config.proto"},
			{Name: "simple_backend", GoName: "SimpleBackend", ProtoType: "message", MessageType: "SimpleBackendConfig", ProtoFile: "config/v1/config.proto"},
		}},
		{Name: "StaticSiteConfig", Fields: []ConfigField{
			{Name: "gcp_project", GoName: "GcpProject", GoType: "string", ProtoType: "string", EnvVar: "STATIC_SITE_GCP_PROJECT", ProtoFile: "config/v1/config.proto"},
			{Name: "base_domain", GoName: "BaseDomain", GoType: "string", ProtoType: "string", EnvVar: "STATIC_SITE_BASE_DOMAIN", ProtoFile: "config/v1/config.proto"},
		}},
		{Name: "SimpleBackendConfig", Fields: []ConfigField{
			{Name: "base_domain", GoName: "BaseDomain", GoType: "string", ProtoType: "string", EnvVar: "SIMPLE_BACKEND_BASE_DOMAIN", ProtoFile: "config/v1/config.proto"},
			{Name: "allowed_image_registries", GoName: "AllowedImageRegistries", GoType: "string", ProtoType: "string", EnvVar: "SIMPLE_BACKEND_ALLOWED_IMAGE_REGISTRIES", ProtoFile: "config/v1/config.proto"},
		}},
	}
}

// TestComposedBlocksWithCollidingLeafNamesStillProject is the regression.
//
// Before the fix, FlattenBlockLeaves did not exist and callers appended each
// block's leaves under their BARE names. Two blocks declaring `base_domain`
// therefore produced two `base_domain` entries in one schema,
// CheckDuplicateConfigFields refused the whole AppConfig, and — because
// `forge generate` downgrades a config-generation failure to a WARNING — the
// previous config_gen.k stayed on disk. The observable result was not an
// error an author would chase: it was EVERY leaf of BOTH blocks silently
// missing from deploy/kcl/config_gen.k, so a tier whose on-switch lived in
// one of them could not be turned on from KCL at all, and the generated file
// looked plausible because the fields had never been there to begin with.
//
// The assertion is per-leaf on purpose. Asserting only that generation
// SUCCEEDS would pass against an implementation that dropped both blocks
// quietly, which is the exact failure being fixed.
func TestComposedBlocksWithCollidingLeafNamesStillProject(t *testing.T) {
	fields := FlattenBlockLeaves(blockCollisionMessages(), "AppConfig")

	out, err := GenerateConfigKCL(fields, "proj")
	if err != nil {
		t.Fatalf("two blocks declaring the same leaf name must still project; got: %v", err)
	}

	for _, env := range []string{
		"STATIC_SITE_BASE_DOMAIN",
		"STATIC_SITE_GCP_PROJECT",
		"SIMPLE_BACKEND_BASE_DOMAIN",
		"SIMPLE_BACKEND_ALLOWED_IMAGE_REGISTRIES",
		"PORT",
	} {
		if !strings.Contains(out, env) {
			t.Errorf("%s missing from the projection — a block leaf was dropped", env)
		}
	}
}

// TestCollidingLeavesAreDistinctSchemaFields pins that the two leaves become
// two SEPARATE KCL fields whose values can differ.
//
// This is the assertion with the teeth. "Both env vars appear" is satisfied
// by an implementation that emits one `base_domain` schema field and reads it
// twice — which would compile, render, and make the static-site and
// simple-backend domains permanently equal. For this tier that is not a
// cosmetic bug: the proto requires simple-backend's domain to be SEPARATE
// from the api host's, because customer-deployed apps run untrusted code and
// a shared parent domain puts the api's cookies in their reach.
//
// Blocks are nested schemas now, so the two leaves keep their proto names
// and are distinguished by the block that holds them.
func TestCollidingLeavesAreDistinctSchemaFields(t *testing.T) {
	fields := FlattenBlockLeaves(blockCollisionMessages(), "AppConfig")

	var paths []string
	for _, f := range fields {
		if f.EnvVar == "STATIC_SITE_BASE_DOMAIN" || f.EnvVar == "SIMPLE_BACKEND_BASE_DOMAIN" {
			paths = append(paths, f.KCLPath())
		}
	}
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("the two base_domain leaves must have distinct KCL paths, got %v", paths)
	}

	out, err := GenerateConfigKCL(fields, "proj")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, want := range []string{
		"schema AppConfigStaticSite:",
		"schema AppConfigSimpleBackend:",
		"    static_site: AppConfigStaticSite = AppConfigStaticSite {}",
		"    simple_backend: AppConfigSimpleBackend = AppConfigSimpleBackend {}",
		`"STATIC_SITE_BASE_DOMAIN" = c.static_site.base_domain`,
		`"SIMPLE_BACKEND_BASE_DOMAIN" = c.simple_backend.base_domain`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("projection missing %q:\n%s", want, out)
		}
	}
}

// TestBlockLeavesAreNeverRenamed pins that no leaf is renamed to dodge a
// collision. The old flattening qualified a leaf only when another block
// claimed the same name, so adding a field to one block could rename a field
// in ANOTHER — silently invalidating every config.k line that authored it.
func TestBlockLeavesAreNeverRenamed(t *testing.T) {
	for _, f := range FlattenBlockLeaves(blockCollisionMessages(), "AppConfig") {
		if strings.Contains(f.Name, "static_site_") || strings.Contains(f.Name, "simple_backend_") {
			t.Errorf("leaf renamed to %q; block leaves keep their proto names", f.Name)
		}
	}
}

// TestDuplicateRefusalStillFiresForRealCollisions pins that this fix did not
// defang CheckDuplicateConfigFields.
//
// That check exists because KCL keeps the LAST declaration of a repeated
// schema field silently, and a shadowed default once shipped an empty APP_URL
// to three prod workloads. Disambiguating BLOCK-QUALIFIED names must not make
// a genuine same-namespace duplicate — one message declaring a name twice —
// projectable.
func TestDuplicateRefusalStillFiresForRealCollisions(t *testing.T) {
	fields := []ConfigField{
		{Name: "app_url", GoName: "AppUrl", GoType: "string", ProtoType: "string", EnvVar: "APP_URL", DefaultValue: "http://localhost:3000", ProtoFile: "a.proto"},
		{Name: "app_url", GoName: "AppUrl", GoType: "string", ProtoType: "string", EnvVar: "APP_URL", ProtoFile: "b.proto"},
	}
	if _, err := GenerateConfigKCL(fields, "proj"); err == nil {
		t.Fatal("a genuine duplicate field name must still be refused")
	}
}
