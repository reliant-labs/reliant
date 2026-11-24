package codemod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const zod3Package = `{
  "name": "web",
  "dependencies": {
    "@hookform/resolvers": "^3.9.0",
    "react-hook-form": "^7.53.0",
    "zod": "^3.23.8"
  }
}
`

const zod3Page = `import { zodResolver } from "@hookform/resolvers/zod";
import { useForm } from "react-hook-form";
import { z } from "zod";

const schema = z.object({
  status: z.coerce.number().pipe(z.nativeEnum(OrderStatus)),
  email: z.string().min(1, "Required").email("Enter a valid email address"),
});
type FormValues = z.infer<typeof schema>;

export function New() {
  const form = useForm<FormValues>({ resolver: zodResolver(schema) });
  return null;
}
`

func zodTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestZod4FormsPortsAZod3Frontend pins NS FG-27: the v1.8.0 playbook's
// mechanical half — the three package versions and the table's edits to a
// generated form — and a second run changes nothing.
func TestZod4FormsPortsAZod3Frontend(t *testing.T) {
	dir := zodTree(t, map[string]string{
		"frontends/web/package.json":           zod3Package,
		"frontends/web/src/app/orders/new.tsx": zod3Page,
		"frontends/web/src/lib/other.tsx":      "export const z = 1; // z.nativeEnum( but no resolver\n",
		"frontends/mobile/package.json":        `{"dependencies": {"zod": "^4.6.6"}}`,
	})
	rep, err := runZod4Forms(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rewrites) != 2 {
		t.Fatalf("rewrites = %q, want the web package.json and its one form", rep.Rewrites)
	}
	pkg, _ := os.ReadFile(filepath.Join(dir, "frontends/web/package.json"))
	for _, want := range []string{`"zod": "^4.6.6"`, `"@hookform/resolvers": "^7.0.7"`, `"react-hook-form": "^7.55.0"`, `"name": "web"`} {
		if !strings.Contains(string(pkg), want) {
			t.Errorf("package.json lacks %s:\n%s", want, pkg)
		}
	}
	page, _ := os.ReadFile(filepath.Join(dir, "frontends/web/src/app/orders/new.tsx"))
	for _, want := range []string{
		"z.coerce.number().pipe(z.enum(OrderStatus))",
		"type FormInput = z.input<typeof schema>;\ntype FormValues = z.output<typeof schema>;",
		"useForm<FormInput, unknown, FormValues>({ resolver: zodResolver(schema) })",
		`.email("Enter a valid email address")`, // deprecated, still working: left
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
	if other, _ := os.ReadFile(filepath.Join(dir, "frontends/web/src/lib/other.tsx")); !strings.Contains(string(other), "z.nativeEnum(") {
		t.Error("a file that calls no zodResolver was rewritten")
	}
	if mobile, _ := os.ReadFile(filepath.Join(dir, "frontends/mobile/package.json")); string(mobile) != `{"dependencies": {"zod": "^4.6.6"}}` {
		t.Errorf("a zod 4 frontend was rewritten: %s", mobile)
	}
	if len(rep.Next) == 0 {
		t.Error("no next step names the install")
	}
	again, err := runZod4Forms(dir, Options{Write: true})
	if err != nil || again.Changed() {
		t.Fatalf("second run: %q, %v; want nothing", again.Rewrites, err)
	}
}

// A zod 4 frontend with an old resolver still needs the input/output type
// split: coerced and defaulted fields can differ before and after parsing.
func TestZod4FormsBumpsAnOldResolver(t *testing.T) {
	const pkg = `{"dependencies": {"zod": "^4.6.6", "@hookform/resolvers": "^3.9.0", "react-hook-form": "^7.60.0"}}`
	page := strings.ReplaceAll(zod3Page, "z.nativeEnum(", "z.enum(")
	migrated := strings.ReplaceAll(page, "type FormValues = z.infer<typeof schema>;", "type FormInput = z.input<typeof schema>;\ntype FormValues = z.output<typeof schema>;")
	migrated = strings.ReplaceAll(migrated, "useForm<FormValues>(", "useForm<FormInput, unknown, FormValues>(")
	dir := zodTree(t, map[string]string{"package.json": pkg, "src/new.tsx": page, "src/ported.tsx": migrated})
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	rep, err := runZod4Forms(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rewrites) != 2 || !strings.Contains(rep.Rewrites[0], "@hookform/resolvers ^3.9.0 → ^7.0.7") || strings.Contains(rep.Rewrites[0], "react-hook-form") {
		t.Fatalf("rewrites = %q, want the resolver and unported form only", rep.Rewrites)
	}
	if read("package.json") != pkg || read("src/new.tsx") != page {
		t.Fatal("a dry run wrote")
	}
	if _, err := runZod4Forms(dir, Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if read("src/new.tsx") != migrated || read("src/ported.tsx") != migrated || read("package.json") != strings.ReplaceAll(pkg, "^3.9.0", "^7.0.7") {
		t.Fatal("resolver-only port did not update exactly the dependency and unported form")
	}
	if again, err := runZod4Forms(dir, Options{Write: true}); err != nil || again.Changed() {
		t.Fatalf("second run = %q, %v, want no changes", again.Rewrites, err)
	}
}

func TestSpecMajor(t *testing.T) {
	for spec, want := range map[string]int{"^3.23.8": 3, "~3.1": 3, "3.x": 3, "3": 3, "npm:zod@^3.22": 3, "^4.6.6": 4, "v4.0.0": 4} {
		if got, ok := specMajor(spec); !ok || got != want {
			t.Errorf("specMajor(%q) = %d, %v; want %d", spec, got, ok, want)
		}
	}
	for _, spec := range []string{">=3.0.0", "*", "^3 || ^4", "latest", ""} {
		if _, ok := specMajor(spec); ok {
			t.Errorf("specMajor(%q) named a single major", spec)
		}
	}
}
