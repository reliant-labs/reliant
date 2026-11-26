package codemod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readEntityAliasFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "entitytypealias", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestEntityTypeAliasRepairsThePagesForgeWrote pins F10/F11 of the outside
// evaluation: an entity named Link made the list page forge scaffolded before
// v1.11.0 declare `Link` twice, and pages are scaffold-once, so only this
// codemod repairs a project that already has one. It must turn the old page
// into exactly what forge writes today — Next.js and Vite — and a second run
// must change nothing.
func TestEntityTypeAliasRepairsThePagesForgeWrote(t *testing.T) {
	c, ok := Lookup("entity-type-alias")
	if !ok {
		t.Fatal("entity-type-alias is not registered")
	}
	files := map[string]string{
		"frontends/web/package.json":                  `{"name":"web"}`,
		"frontends/web/src/app/links/page.tsx":        readEntityAliasFixture(t, "next-list.before.tsx"),
		"frontends/spa/package.json":                  `{"name":"spa"}`,
		"frontends/spa/src/pages/links/List.tsx":      readEntityAliasFixture(t, "vite-list.before.tsx"),
		"frontends/web/src/app/tasks/page.tsx":        taskPageNoCollision,
		"frontends/web/src/gen/services/x/v1/x_pb.ts": "export type Link = { id: string };\nimport Link from \"next/link\";\n",
	}
	dir := writeTree(t, files)

	dry, err := c.Run(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Rewrites) != 2 || len(dry.Next) == 0 {
		t.Fatalf("dry run: want 2 rewrites and remaining references to check, got %+v", dry)
	}
	if got := readFile(t, dir, "frontends/web/src/app/links/page.tsx"); got != files["frontends/web/src/app/links/page.tsx"] {
		t.Fatal("a dry run wrote the page")
	}

	rep, err := c.Run(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"frontends/web/src/app/links/page.tsx: Link → LinkEntity (2 type use(s))",
		"frontends/spa/src/pages/links/List.tsx: Link → LinkEntity (2 type use(s))",
	} {
		if !containsLine(rep.Rewrites, want) {
			t.Errorf("rewrites %q should include %q", rep.Rewrites, want)
		}
	}
	if got, want := readFile(t, dir, "frontends/web/src/app/links/page.tsx"), readEntityAliasFixture(t, "next-list.after.tsx"); got != want {
		t.Errorf("Next.js page after the codemod differs from what forge writes today:\n%s", got)
	}
	if got, want := readFile(t, dir, "frontends/spa/src/pages/links/List.tsx"), readEntityAliasFixture(t, "vite-list.after.tsx"); got != want {
		t.Errorf("Vite page after the codemod differs from what forge writes today:\n%s", got)
	}
	// No collision, a generated module: neither is the codemod's to touch.
	for _, rel := range []string{"frontends/web/src/app/tasks/page.tsx", "frontends/web/src/gen/services/x/v1/x_pb.ts"} {
		if got := readFile(t, dir, rel); got != files[rel] {
			t.Errorf("%s was rewritten:\n%s", rel, got)
		}
	}

	again, err := c.Run(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() || len(again.Next) > 0 {
		t.Errorf("second run is not a no-op: %+v", again)
	}
}

const taskPageNoCollision = `import Link from "next/link";

import type { Task } from "@/gen/services/tasks/v1/tasks_pb";

const columns: ResourceColumn<Task>[] = [];
`

// A use the codemod cannot tell from the other import's value is named, with
// its line, rather than guessed; the unambiguous uses are still renamed.
func TestEntityTypeAliasNamesTheUsesItCannotClassify(t *testing.T) {
	const page = `import { Link, useNavigate } from "@tanstack/react-router";

import type { Link } from "@/gen/services/links/v1/links_pb";

// A comment about Link: not code.
const pick = (item: Link) => item.id;
const many: Link[] = [];
const pair: Map<string, Link> = new Map();
const nav = <Link to="/x">Link</Link>;
`
	edit := aliasCollidingEntityTypes("src/x.tsx", page)
	if edit == nil {
		t.Fatal("the collision was not detected")
	}
	for _, want := range []string{
		`import type { Link as LinkEntity } from "@/gen/services/links/v1/links_pb";`,
		"const many: LinkEntity[] = [];",
		"const pair: Map<string, LinkEntity> = new Map();",
		`const nav = <Link to="/x">Link</Link>;`,
		`import { Link, useNavigate } from "@tanstack/react-router";`,
		"const pick = (item: Link) => item.id;",
	} {
		if !strings.Contains(edit.content, want) {
			t.Errorf("rewritten file should contain %q:\n%s", want, edit.content)
		}
	}
	if len(edit.next) != 2 || !strings.HasPrefix(edit.next[0], "src/x.tsx:6: `const pick = (item: Link) => item.id;`") ||
		!strings.HasPrefix(edit.next[1], "src/x.tsx:9:") {
		t.Errorf("next = %q, want the annotation and JSX references", edit.next)
	}
}

// Unsupported syntax must always be reported rather than silently left behind
// after changing the import. Do not guess whether a reference means the entity
// or the component: both can have a type in TypeScript.
func TestEntityTypeAliasReportsEveryUnrepairedReference(t *testing.T) {
	for _, use := range []string{
		"const x: Link = value;",
		"type X = Link;",
		"type X = Link | Other;",
		"type X = Other & Link;",
		"type X = [Link, Other];",
		"type X = T extends Other ? Link : never;",
		"type X = Wrapper<Inner<Other>, Link>;",
		"const x = value as Link;",
		"const x = value satisfies Link;",
		"type X =\n  Link;",
		"const x = Link;",
		"const x = <Link />;",
	} {
		t.Run(use, func(t *testing.T) {
			page := "import Link from 'next/link';\nimport type { Link } from './links_pb';\n" + use + "\n"
			edit := aliasCollidingEntityTypes("src/x.tsx", page)
			if edit == nil || len(edit.renamed) != 1 || len(edit.next) != 1 {
				t.Fatalf("unrepaired reference must be reported: %+v", edit)
			}
			if !strings.Contains(edit.content, use) || !strings.HasPrefix(edit.next[0], "src/x.tsx:") {
				t.Fatalf("expected unchanged use and file:line guidance: %+v", edit)
			}
		})
	}
	// This example in B1 is already covered by the generic-argument rewrite.
	edit := aliasCollidingEntityTypes("src/x.tsx", "import Link from 'next/link';\nimport type { Link } from './links_pb';\ntype X = Promise<Link>;\n")
	if edit == nil || !strings.Contains(edit.content, "Promise<LinkEntity>") || len(edit.next) != 0 {
		t.Fatalf("simple generic should be repaired: %+v", edit)
	}
}

// A file that already binds the alias is not edited: renaming into a name in
// use would trade one duplicate identifier for another.
func TestEntityTypeAliasLeavesAFileWhoseAliasIsTaken(t *testing.T) {
	const page = `import Link from "next/link";
import { LinkEntity } from "@/components/link-entity";

import type { Link } from "@/gen/services/links/v1/links_pb";
`
	edit := aliasCollidingEntityTypes("src/x.tsx", page)
	if edit == nil || len(edit.renamed) != 0 || len(edit.next) != 1 || !strings.Contains(edit.next[0], "LinkEntity is already bound") {
		t.Fatalf("want no rename and one note, got %+v", edit)
	}
}

// --scope bounds the files the run rewrites, as `forge lint --scope` does.
func TestEntityTypeAliasHonoursScope(t *testing.T) {
	c, _ := Lookup("entity-type-alias")
	before := readEntityAliasFixture(t, "next-list.before.tsx")
	dir := writeTree(t, map[string]string{
		"frontends/web/package.json":           `{"name":"web"}`,
		"frontends/web/src/app/links/page.tsx": before,
		"frontends/web/src/app/other/page.tsx": before,
	})
	scope, err := ParseScope(dir, []string{"frontends/web/src/app/links"})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Run(dir, Options{Write: true, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rewrites) != 1 || !strings.HasPrefix(rep.Rewrites[0], "frontends/web/src/app/links/page.tsx:") {
		t.Errorf("rewrites = %q, want only the scoped page", rep.Rewrites)
	}
	if got := readFile(t, dir, "frontends/web/src/app/other/page.tsx"); got != before {
		t.Error("a page outside the scope was rewritten")
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
