package cli

import (
	"testing"
)

// The v1.11.0 entity-type-alias playbook runs `forge fix entity-type-alias`,
// and its detection is that codemod's own decision (forge-detect), so it is
// offered exactly to a project with a page whose entity type import collides
// with another import of the file.
func TestV1110EntityTypeAliasPlaybook_DetectionIsTheCodemods(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, meta := range metas {
		if meta.ID == "v1.11.0/entity-type-alias" {
			m = meta
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped playbook v1.11.0/entity-type-alias")
	}
	if m.Version != "v1.11.0" || m.SkillPath != "migrations/v1.11.0/entity-type-alias" {
		t.Errorf("meta = %+v", m)
	}
	if len(m.Automation) != 1 || m.Automation[0] != "fix:entity-type-alias" {
		t.Errorf("automation = %v, want [fix:entity-type-alias]", m.Automation)
	}
	const (
		colliding = "import Link from \"next/link\";\n\nimport type { Link } from \"@/gen/services/links/v1/links_pb\";\n\nconst columns: ResourceColumn<Link>[] = [];\n"
		aliased   = "import Link from \"next/link\";\n\nimport type { Link as LinkEntity } from \"@/gen/services/links/v1/links_pb\";\n\nconst columns: ResourceColumn<LinkEntity>[] = [];\n"
		task      = "import Link from \"next/link\";\n\nimport type { Task } from \"@/gen/services/tasks/v1/tasks_pb\";\n"
	)
	for name, tc := range map[string]struct {
		page string // frontends/web/src/app/x/page.tsx; "" writes none
		want bool
	}{
		"no frontend":              {"", false},
		"a page that collides":     {colliding, true},
		"a page already aliased":   {aliased, false},
		"a page with no collision": {task, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.page != "" {
				writeProjectFile(t, dir, "frontends/web/package.json", `{"name":"web"}`)
				writeProjectFile(t, dir, "frontends/web/src/app/x/page.tsx", tc.page)
			}
			if got := migrationApplies(m, "v1.10.0", dir); got != tc.want {
				t.Errorf("applies = %v, want %v", got, tc.want)
			}
		})
	}
}
