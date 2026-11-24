package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// dialectFixture is one frontend directory: its package.json and whatever
// else is installed beside it (relative path → contents).
type dialectFixture struct {
	pkg   string            // "" = no package.json
	files map[string]string // node_modules/…, package-lock.json, pnpm-lock.yaml
	want  codegen.FormDialect
	warns int // warnings frontendFormDialect returns
}

func (f dialectFixture) write(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{}
	for k, v := range f.files {
		files[k] = v
	}
	if f.pkg != "" {
		files["package.json"] = f.pkg
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	zod3Dialect = codegen.FormDialect{Zod3: true, LegacyResolver: true}
	zod3Only    = codegen.FormDialect{Zod3: true}
)

func installedZod(v string) map[string]string {
	return map[string]string{"node_modules/zod/package.json": fmt.Sprintf(`{"name": "zod", "version": %q}`, v)}
}

// dialectFixtures is the ONE table `forge generate`'s dialect reading and
// the v1.8.0 migration's detection are both held to: the playbook is
// offered exactly where generate writes a non-default dialect.
func dialectFixtures() map[string]dialectFixture {
	f := map[string]dialectFixture{
		"scaffolded today": {pkg: `{"dependencies": {"zod": "^4.6.6", "@hookform/resolvers": "^7.0.7"}}`},
		"scaffolded before zod 4": {pkg: `{"dependencies": {"zod": "^3.23.0", "@hookform/resolvers": "^3.9.0"}}`,
			want: zod3Dialect},
		// Half-upgraded: resolvers 5 accepts zod 3 and already types the
		// form by input and output.
		"zod 3 with resolvers 5":                {pkg: `{"dependencies": {"zod": "~3.25.76", "@hookform/resolvers": "^5.2.2"}}`, want: zod3Only},
		"zod 4 with old resolvers":              {pkg: `{"dependencies": {"zod": "^4.6.6", "@hookform/resolvers": "~3.10.0"}}`, want: codegen.FormDialect{LegacyResolver: true}},
		"a later major":                         {pkg: `{"dependencies": {"zod": "^13.0.0", "@hookform/resolvers": "^10.0.0"}}`},
		"dependencies win over devDependencies": {pkg: `{"dependencies": {"zod": "^4.6.6"}, "devDependencies": {"zod": "^3.23.0"}}`},
		"no zod at all":                         {pkg: `{"dependencies": {"react": "^19.1.0"}}`},
		"no package.json":                       {},
		"package.json is not JSON":              {pkg: `{`, warns: 1},

		// An open range is what is installed.
		"open range, zod 3 in node_modules": {pkg: `{"dependencies": {"zod": ">=3.0.0"}}`, files: installedZod("3.25.76"), want: zod3Only},
		"open range, zod 4 in node_modules": {pkg: `{"dependencies": {"zod": ">=3.0.0"}}`, files: installedZod("4.6.6")},
		"catalog:, zod 3 in package-lock": {pkg: `{"dependencies": {"zod": "catalog:"}}`,
			files: map[string]string{"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {}, "node_modules/zod": {"version": "3.22.4"}}}`},
			want:  zod3Only},
		"star, zod 3 in a v1 package-lock": {pkg: `{"dependencies": {"zod": "*"}}`,
			files: map[string]string{"package-lock.json": `{"lockfileVersion": 1, "dependencies": {"zod": {"version": "3.22.4"}}}`},
			want:  zod3Only},
		"latest, zod 3 in pnpm-lock v9": {pkg: `{"devDependencies": {"zod": "latest"}}`,
			files: map[string]string{"pnpm-lock.yaml": "lockfileVersion: '9.0'\nimporters:\n  .:\n    devDependencies:\n      zod:\n        specifier: latest\n        version: 3.25.76(typescript@5.6.3)\n"},
			want:  zod3Only},
		"union, zod 3 in pnpm-lock v5": {pkg: `{"dependencies": {"zod": "^3 || ^4"}}`,
			files: map[string]string{"pnpm-lock.yaml": "lockfileVersion: 5.4\ndependencies:\n  zod: 3.22.4\n"},
			want:  zod3Only},
		// A single-major range is the declaration; a stale install does
		// not override it.
		"declared 3, stale 4 installed": {pkg: `{"dependencies": {"zod": "^3.23.0"}}`, files: installedZod("4.6.6"), want: zod3Only},
		// Unsettled: today's default, said out loud.
		"open range, nothing installed": {pkg: `{"dependencies": {"zod": ">=3.0.0", "@hookform/resolvers": "workspace:*"}}`, warns: 2},
		"git spec, nothing installed":   {pkg: `{"dependencies": {"zod": "github:colinhacks/zod"}}`, warns: 1},
	}
	// Every single-major spelling of zod 3, in both dependency maps.
	for _, spec := range []string{"^3.23.0", "~3.22", "~3.22.4", "3.x", "3.X", "3.*", "3", "3.25.76", "v3", "v3.23.8", "=3.1.0", "=v3.1.0",
		"~>3.2", ">=3.0.0 <4", ">= 3.0.0 < 4.0.0", ">=3.2 <4.0.0-0", "3.0.0 - 3.25.76", "3 - 3.9", "<4 >=3.22", "npm:zod@^3.22", "workspace:^3.1", "^3.22 || ~3.25"} {
		for _, deps := range []string{"dependencies", "devDependencies"} {
			f[fmt.Sprintf("zod %q in %s", spec, deps)] = dialectFixture{
				pkg: fmt.Sprintf(`{%q: {"zod": %q, "@hookform/resolvers": "^7.0.7"}}`, deps, spec), want: zod3Only}
		}
	}
	// … and of zod 4.
	for _, spec := range []string{"^4.6.6", "~4.1", "4.x", "4", "v4", "v4.6.6", ">=4.0.0 <5", "4.0.0 - 4.9.9", ">3.x <5", "npm:zod@^4.6.6"} {
		for _, deps := range []string{"dependencies", "devDependencies"} {
			f[fmt.Sprintf("zod %q in %s", spec, deps)] = dialectFixture{
				pkg: fmt.Sprintf(`{%q: {"zod": %q, "@hookform/resolvers": "^7.0.7"}}`, deps, spec)}
		}
	}
	return f
}

// TestFrontendFormDialect reads the dialect a page forge adds to an existing
// frontend is written in from that frontend's own packages: one that
// predates zod 4 keeps getting forms its packages compile.
func TestFrontendFormDialect(t *testing.T) {
	for name, tc := range dialectFixtures() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			tc.write(t, dir)
			got, warns := frontendFormDialect(dir)
			if got != tc.want {
				t.Errorf("frontendFormDialect = %+v, want %+v", got, tc.want)
			}
			if len(warns) != tc.warns {
				t.Errorf("warnings = %q, want %d", warns, tc.warns)
			}
			for _, w := range warns {
				if !strings.Contains(w, filepath.Join(dir, "package.json")) {
					t.Errorf("warning %q does not name the package.json", w)
				}
			}
		})
	}
}

// TestFrontendFormDialect_HoistedNodeModules: a workspace hoists zod to its
// root, and Node resolves it by walking up — so does the dialect reading.
func TestFrontendFormDialect_HoistedNodeModules(t *testing.T) {
	root := t.TempDir()
	dialectFixture{files: installedZod("3.25.76")}.write(t, root)
	fe := filepath.Join(root, "frontends", "web")
	dialectFixture{pkg: `{"dependencies": {"zod": "catalog:"}}`}.write(t, fe)
	if got, warns := frontendFormDialect(fe); got != zod3Only || len(warns) != 0 {
		t.Errorf("frontendFormDialect = %+v, %q; want %+v, no warnings", got, warns, zod3Only)
	}
}

func TestDeclaredMajor(t *testing.T) {
	for spec, want := range map[string]int{
		"^4.6.6": 4, "~3.25": 3, "3.x": 3, "v4": 4, " ^10.1 ": 10, "v3.23.8": 3, "3": 3, "=3.1.0": 3,
		">=3.0.0 <4": 3, ">=3.0.0 <4.0.0": 3, ">=3.0.0 <=3.99": 3, ">3.x <5": 4, "<4 >=3": 3,
		"3.0.0 - 3.25.76": 3, "3 - 4": -1, "^0.9": 0, "npm:zod@^3.22": 3, "npm:@scope/zod@~4.1": 4,
		"workspace:^3.1": 3, "^3.22 || ~3.25": 3, "^3 || <0": 3, "4.0.0-beta.1": 4, "<4.0.1 >=4": 4,
	} {
		got, ok := declaredMajor(spec)
		if want < 0 {
			if ok {
				t.Errorf("declaredMajor(%q) = %d, true; want no single major", spec, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("declaredMajor(%q) = %d, %v; want %d", spec, got, ok, want)
		}
	}
	for _, spec := range []string{
		"", "*", "x", "latest", "next", "workspace:*", "workspace:^", "catalog:", "catalog:default", "github:colinhacks/zod",
		"git+https://github.com/colinhacks/zod.git", "file:../zod", "link:../zod", "https://example.com/zod.tgz", "npm:zod",
		">=3.0.0", ">3.0.0", "^3 || ^4", ">=3 <5", "<4", "<=4.2", "3.x || 4.x", "<3 >=4",
	} {
		if got, ok := declaredMajor(spec); ok {
			t.Errorf("declaredMajor(%q) = %d, true; want no single major", spec, got)
		}
	}
}

func TestRangeMajors(t *testing.T) {
	for spec, want := range map[string][2]int{
		">=3.0.0":   {3, unboundedMajor},
		">3":        {4, unboundedMajor},
		">3.0.0":    {3, unboundedMajor},
		"<4":        {0, 3},
		"<4.0":      {0, 3},
		"<4.0.0":    {0, 3},
		"<4.0.0-0":  {0, 3},
		"<4.0.0-rc": {0, 4},
		"<4.1":      {0, 4},
		"<=4":       {0, 4},
		"^3 || ^5":  {3, 5},
		"":          {0, unboundedMajor},
		"*":         {0, unboundedMajor},
	} {
		lo, hi, ok := rangeMajors(spec)
		if !ok || lo != want[0] || hi != want[1] {
			t.Errorf("rangeMajors(%q) = %d, %d, %v; want %d, %d", spec, lo, hi, ok, want[0], want[1])
		}
	}
	for _, spec := range []string{"<0", "<3 >=4", "^3 foo", "latest"} {
		if lo, hi, ok := rangeMajors(spec); ok {
			t.Errorf("rangeMajors(%q) = %d, %d, true; want unreadable", spec, lo, hi)
		}
	}
}

// scaffoldedPackageJSON is a frontend kind's package.json as forge
// scaffolds it today, with the template's optional workspace block dropped
// so it parses as JSON.
func scaffoldedPackageJSON(t *testing.T, kind string) string {
	t.Helper()
	raw, err := templates.FrontendTemplates().Get(kind + "/package.json.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if start, end := strings.Index(text, "{{- if .Workspaces}}"), strings.Index(text, "{{- end}}"); start >= 0 && end > start {
		text = text[:start] + text[end+len("{{- end}}"):]
	}
	return text
}

// TestZod4Migration_Detection runs the SHIPPED v1.8.0 detection over the
// same fixtures the dialect reading is held to, for a frontend under
// frontends/ and for a site at the project root: the playbook is offered
// exactly where `forge generate` would write pages in a pre-zod-4 dialect.
// A frontend scaffolded today is offered nothing, and reads as today's
// dialect.
func TestZod4Migration_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.SkillPath == zod4MigrationSkill {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatalf("no shipped migration at %s — `forge generate` points users at it", zod4MigrationSkill)
	}

	cases := dialectFixtures()
	for _, kind := range []string{"nextjs", "vite-spa", "react-native"} {
		cases["scaffolded today: "+kind] = dialectFixture{pkg: scaffoldedPackageJSON(t, kind)}
	}
	for name, tc := range cases {
		for _, rel := range []string{"frontends/web", "."} {
			t.Run(name+" at "+rel, func(t *testing.T) {
				root := t.TempDir()
				dir := filepath.Join(root, rel)
				tc.write(t, dir)
				got, _ := frontendFormDialect(dir)
				if got != tc.want {
					t.Errorf("frontendFormDialect = %+v, want %+v", got, tc.want)
				}
				want := tc.want != (codegen.FormDialect{})
				if applies := migrationApplies(m, "v1.7.0", root); applies != want {
					t.Errorf("migration applies = %v, but generate reads the frontend as %+v", applies, got)
				}
			})
		}
	}
}

// TestZod4Migration_EveryDetectedShapeHasASection: whatever the detection
// offers the playbook for has a section of the playbook that says so — zod 3
// is section 1, zod 4 with a resolver below 5 is section 2 — and the
// resolver-only frontend is detected at all.
func TestZod4Migration_EveryDetectedShapeHasASection(t *testing.T) {
	body, err := templates.ProjectTemplates().Get("skills/forge/" + zod4MigrationSkill + "/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	sections := map[codegen.FormDialect]string{
		zod3Dialect:            "## 1. Frontends move to zod 4",
		zod3Only:               "## 1. Frontends move to zod 4",
		{LegacyResolver: true}: "## 2. A zod 4 frontend still on @hookform/resolvers below 5",
	}
	useWhen := map[string]string{
		"## 1. Frontends move to zod 4":                               "**Use when** a frontend declares `zod` 3",
		"## 2. A zod 4 frontend still on @hookform/resolvers below 5": "**Use when** a frontend declares `zod` 4 but `@hookform/resolvers` below 5",
	}
	for heading, use := range useWhen {
		i := strings.Index(text, heading)
		if i < 0 {
			t.Fatalf("playbook has no section %q", heading)
		}
		if !strings.Contains(text[i:], use) {
			t.Errorf("section %q does not say %q", heading, use)
		}
	}

	root := t.TempDir()
	dialectFixture{pkg: `{"dependencies": {"zod": "^4.6.6", "@hookform/resolvers": "^3.9.0"}}`}.write(t, filepath.Join(root, "frontends", "web"))
	if !projectHasPreZod4Frontend(root) {
		t.Error("a zod 4 frontend on @hookform/resolvers 3 is not offered section 2")
	}

	for name, tc := range dialectFixtures() {
		if tc.want == (codegen.FormDialect{}) {
			continue
		}
		if _, ok := sections[tc.want]; !ok {
			t.Errorf("%s: dialect %+v is detected, but no playbook section covers it", name, tc.want)
		}
	}
}

func TestForgeDetect_UnknownDetectorDoesNotApply(t *testing.T) {
	if runDetection(t.TempDir(), "forge-detect no-such-detector") {
		t.Error("an unknown detector read as applying")
	}
	if runDetection(t.TempDir(), "forge-detect") {
		t.Error("forge-detect with no detector read as applying")
	}
}
