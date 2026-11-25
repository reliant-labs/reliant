//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EFrameworkNamedEntitiesTypecheck scaffolds entities named like what
// the generated pages import for themselves — Link, Image, Head, Script and
// Form are next/* default exports — and requires the frontend to type-check.
//
// An outside evaluator's first project ("linkbox", entity Link) failed here on
// v1.10.0: the list page bound the entity type as `import type { Link }` beside
// `import Link from "next/link"`. Pages now bind it as `Link as LinkEntity`
// (naming.EntityTypeLocal). An enum is named by the user too: Link's `style`
// is an enum `Button`, which the create and edit pages would otherwise bind
// beside the component library's Button; pages bind it as `Button as
// ButtonEnum` (naming.EnumTypeLocal).
//
// Pages are scaffold-once, so the second half proves the repair for a project
// that already has the old page: it writes the pre-v1.11.0 import back, checks
// tsc refuses it, runs `forge fix entity-type-alias`, and requires the page to
// come back byte for byte and type-check again.
//
// node/npm are required via requireTool (skip on a laptop, FAIL in CI),
// mirroring TestE2EDigitNamedServicesAndEntitiesBuild.
func TestE2EFrameworkNamedEntitiesTypecheck(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "linkbox",
		"--mod", "example.com/linkbox",
		"--service", "links",
		"--frontend", "web",
	)
	projectDir := filepath.Join(dir, "linkbox")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "links", "v1", "links.proto")
	var entities strings.Builder
	for _, name := range []string{"Link", "Image", "Head", "Script", "Form"} {
		style := ""
		if name == "Link" {
			style = "  Button style = 3;\n"
		}
		entities.WriteString("\n// forge:entity\nmessage " + name + " {\n  string id = 1;\n  string title = 2;\n" + style + "}\n")
	}
	entities.WriteString("\nenum Button {\n  BUTTON_UNSPECIFIED = 0;\n  BUTTON_PRIMARY = 1;\n  BUTTON_GHOST = 2;\n}\n")
	if err := os.WriteFile(protoPath, []byte(readFileE2E(t, protoPath)+entities.String()), 0o644); err != nil {
		t.Fatalf("author %s: %v", protoPath, err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")
	runCmd(t, projectDir, forgeBin, "generate")

	webDir := filepath.Join(projectDir, "frontends", "web")
	pagePath := filepath.Join(webDir, "src", "app", "links", "page.tsx")
	page := readFileE2E(t, pagePath)
	if !strings.Contains(page, "import type { Link as LinkEntity }") {
		t.Fatalf("links list page does not alias the entity type:\n%s", page)
	}
	createPath := filepath.Join(webDir, "src", "app", "links", "new", "page.tsx")
	if create := readFileE2E(t, createPath); !strings.Contains(create, "Button as ButtonEnum") || !strings.Contains(create, "ButtonEnum.PRIMARY") {
		t.Fatalf("links create page does not alias the enum type Button:\n%s", create)
	}

	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, webDir, 5*time.Minute,
		"pnpm", "install", "--no-frozen-lockfile", "--prefer-offline")
	runCmdTimeout(t, webDir, 3*time.Minute,
		"npx", "--no-install", "tsc", "--noEmit")

	// ── the page an older forge wrote, and its repair ──
	old := strings.ReplaceAll(strings.Replace(page, "Link as LinkEntity", "Link", 1), "LinkEntity", "Link")
	if err := os.WriteFile(pagePath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCmdAllowFail(t, webDir, "npx", "--no-install", "tsc", "--noEmit"); err == nil || !strings.Contains(out, "Duplicate identifier 'Link'") {
		t.Fatalf("tsc should refuse the pre-v1.11.0 page with Duplicate identifier 'Link' (err=%v):\n%s", err, out)
	}
	runCmd(t, projectDir, forgeBin, "fix", "entity-type-alias")
	if got := readFileE2E(t, pagePath); got != page {
		t.Errorf("forge fix entity-type-alias did not restore the page forge writes today:\n%s", got)
	}
	runCmdTimeout(t, webDir, 3*time.Minute,
		"npx", "--no-install", "tsc", "--noEmit")
}
