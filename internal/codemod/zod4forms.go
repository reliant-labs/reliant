package codemod

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The v1.8.0 zod 4 port (NS FG-27): GHSA-mrhx-c7xc-m3w5 (high) covers every
// zod below 4.6.6 and has no 3.x fix, so a frontend scaffolded before v1.8.0
// fails `forge ci vuln-scan --npm` until it moves to zod 4. The migration
// skill (migrations/v1.8.0) is the story; this is its mechanical half:
//
//   - package.json: zod to ^4.6.6, @hookform/resolvers to ^7.0.7 and
//     react-hook-form to ^7.55.0 (resolvers 5+ needs it), the versions a
//     frontend scaffolded since v1.8.0 pins. Each is rewritten in place, so
//     the file's own formatting and key order stay.
//   - every .tsx under src/ that calls zodResolver: the three edits of the
//     skill's table a generated create/edit page needs — z.nativeEnum( to
//     z.enum(, the form typed by z.input and z.output, and useForm given both.
//
// What it leaves: installing (the frontend's own package manager), `.email()`
// (deprecated in zod 4, still working; the skill shows the pipe form), and
// any zod code of the project's own beyond those three shapes. The report
// names each as a next step.

const (
	zod4Spec          = "^4.6.6"
	zod4ResolversSpec = "^7.0.7"
	zod4RHFSpec       = "^7.55.0"
)

func init() {
	register(Codemod{
		Name:    "zod4-forms",
		Version: "v1.8.0",
		Short:   "Move frontends from zod 3 to zod 4 (GHSA-mrhx-c7xc-m3w5): bump the packages and port the generated forms",
		Long: `Move each frontend that declares zod 3 (or zod 4 with an @hookform/resolvers
below 5) to the versions a frontend scaffolded since v1.8.0 pins, and port its
create and edit forms to the zod 4 API.

GHSA-mrhx-c7xc-m3w5 (high) covers every zod below 4.6.6 and there is no 3.x
fix, so ` + "`forge ci vuln-scan --npm`" + ` fails such a frontend until it moves.

In package.json it sets zod ^4.6.6, @hookform/resolvers ^7.0.7 and
react-hook-form ^7.55.0, editing only those values. In every .tsx under src/
that calls zodResolver it makes the mechanical edits of the migration skill's
table: z.nativeEnum( becomes z.enum(, ` + "`type FormValues = z.infer<typeof schema>`" + `
becomes the z.input / z.output pair, and useForm<FormValues>( becomes
useForm<FormInput, unknown, FormValues>(. It installs nothing, and leaves
.email() (deprecated, still working) and the rest of your own zod code to the
skill's section 1, step 3. Re-running it changes nothing.`,
		CleanTree: true,
		Scoped:    true,
		Run:       runZod4Forms,
	})
}

var (
	zodDepRe       = regexp.MustCompile(`("(zod|@hookform/resolvers|react-hook-form)"\s*:\s*)"([^"]*)"`)
	nativeEnumRe   = regexp.MustCompile(`\bz\.nativeEnum\(`)
	inferValuesRe  = regexp.MustCompile(`(?m)^type FormValues = z\.infer<typeof schema>;$`)
	useFormValueRe = regexp.MustCompile(`\buseForm<FormValues>\(`)
)

func runZod4Forms(dir string, opts Options) (Report, error) {
	var rep Report
	frontends, err := zodFrontendDirs(dir)
	if err != nil {
		return rep, err
	}
	for _, fe := range frontends {
		pkgRel := filepath.ToSlash(filepath.Join(fe, "package.json"))
		if !opts.Scope.Contains(pkgRel) {
			continue
		}
		pkgPath := filepath.Join(dir, filepath.FromSlash(pkgRel))
		raw, err := os.ReadFile(pkgPath) //nolint:gosec // a project frontend's package.json
		if err != nil {
			continue
		}
		deps := map[string]string{}
		for _, m := range zodDepRe.FindAllStringSubmatch(string(raw), -1) {
			deps[m[2]] = m[3]
		}
		zodMajor, zodKnown := specMajor(deps["zod"])
		resMajor, resKnown := specMajor(deps["@hookform/resolvers"])
		if _, declared := deps["zod"]; !declared {
			continue
		}
		zod3 := zodKnown && zodMajor < 4
		oldResolvers := zodKnown && zodMajor >= 4 && resKnown && resMajor < 5
		if !zodKnown && deps["zod"] != "" {
			rep.Next = append(rep.Next, fmt.Sprintf("%s declares zod %q, which names no single major: set it to %q by hand if it resolves below 4.6.6", pkgRel, deps["zod"], zod4Spec))
			continue
		}
		if !zod3 && !oldResolvers {
			continue
		}
		want := map[string]string{"zod": zod4Spec}
		if _, ok := deps["@hookform/resolvers"]; ok {
			want["@hookform/resolvers"] = zod4ResolversSpec
		}
		if spec, ok := deps["react-hook-form"]; ok && !atLeast(spec, 7, 55) {
			want["react-hook-form"] = zod4RHFSpec
		}
		var bumped []string
		out := zodDepRe.ReplaceAllStringFunc(string(raw), func(s string) string {
			m := zodDepRe.FindStringSubmatch(s)
			if spec, ok := want[m[2]]; ok && m[3] != spec {
				bumped = append(bumped, fmt.Sprintf("%s %s → %s", m[2], m[3], spec))
				return m[1] + strconv.Quote(spec)
			}
			return s
		})
		if len(bumped) > 0 {
			sort.Strings(bumped)
			if opts.Write {
				if err := atomicfile.WriteFile(pkgPath, []byte(out), 0o644); err != nil {
					return rep, err
				}
			}
			rep.Rewrites = append(rep.Rewrites, pkgRel+": "+strings.Join(bumped, ", "))
		}
		// Upgrading an old resolver changes how it types the schema input,
		// even when this frontend already declares Zod 4.
		if err := portZod4Forms(dir, fe, opts, &rep); err != nil {
			return rep, err
		}
		rep.Next = append(rep.Next, fmt.Sprintf("in %s, install with the frontend's package manager (pnpm install, else npm install) and check nothing resolves zod below 4.6.6 (npm ls zod)", feLabel(fe)))
	}
	if len(rep.Next) > 0 && rep.Changed() {
		rep.Next = append(rep.Next, "port the rest of your own zod code (migrations/v1.8.0, section 1, step 3: error customization, top-level formats such as z.email(), .default, z.record), then run `npx tsc --noEmit` and the build in each frontend")
		rep.Verify = []string{"npx tsc --noEmit", "npm run build"}
	}
	return rep, nil
}

// portZod4Forms applies the table's mechanical edits to every .tsx under the
// frontend's src/ that uses zodResolver.
func portZod4Forms(dir, fe string, opts Options, rep *Report) error {
	src := filepath.Join(dir, filepath.FromSlash(fe), "src")
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == src {
				return nil // no src/: nothing to port
			}
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if !opts.Scope.Contains(rel) {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // a project source file
		if err != nil {
			return err
		}
		s := string(b)
		if !strings.Contains(s, "zodResolver") {
			return nil
		}
		var edits []string
		if nativeEnumRe.MatchString(s) {
			s = nativeEnumRe.ReplaceAllString(s, "z.enum(")
			edits = append(edits, "z.nativeEnum → z.enum")
		}
		if inferValuesRe.MatchString(s) {
			s = inferValuesRe.ReplaceAllString(s, "type FormInput = z.input<typeof schema>;\ntype FormValues = z.output<typeof schema>;")
			edits = append(edits, "FormValues → z.input / z.output")
		}
		if useFormValueRe.MatchString(s) {
			s = useFormValueRe.ReplaceAllString(s, "useForm<FormInput, unknown, FormValues>(")
			edits = append(edits, "useForm<FormInput, unknown, FormValues>")
		}
		if len(edits) == 0 {
			return nil
		}
		if opts.Write {
			if err := atomicfile.WriteFile(path, []byte(s), 0o644); err != nil {
				return err
			}
		}
		rep.Rewrites = append(rep.Rewrites, rel+": "+strings.Join(edits, ", "))
		return nil
	})
}

// zodFrontendDirs lists the project-relative directories that may hold a
// frontend: the root (a site) and each frontends/<name>.
func zodFrontendDirs(dir string) ([]string, error) {
	out := []string{"."}
	matches, err := filepath.Glob(filepath.Join(dir, "frontends", "*", "package.json"))
	if err != nil {
		return nil, err
	}
	for _, m := range matches {
		rel, err := filepath.Rel(dir, filepath.Dir(m))
		if err != nil {
			return nil, err
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

func feLabel(fe string) string {
	if fe == "." {
		return "the project root"
	}
	return fe
}

// specMajor reads the single major an npm range names (^3.22.4, ~3.1, 3.x,
// 3, npm:zod@^3.22): false for a range that admits several (>=3, *, a
// union) or none.
func specMajor(spec string) (int, bool) {
	spec = strings.TrimSpace(spec)
	if i := strings.LastIndex(spec, "@"); i >= 0 && strings.HasPrefix(spec, "npm:") {
		spec = spec[i+1:]
	}
	if spec == "" || strings.ContainsAny(spec, "<>|*") || strings.Contains(spec, " - ") {
		return 0, false
	}
	spec = strings.TrimLeft(spec, "^~=v")
	j := 0
	for j < len(spec) && spec[j] >= '0' && spec[j] <= '9' {
		j++
	}
	if j == 0 || (j < len(spec) && spec[j] != '.') {
		return 0, false
	}
	n, err := strconv.Atoi(spec[:j])
	return n, err == nil
}

// atLeast reports whether an npm range's lowest version is at least
// major.minor (a range it cannot read counts as not).
func atLeast(spec string, major, minor int) bool {
	spec = strings.TrimLeft(strings.TrimSpace(spec), "^~=v")
	parts := strings.SplitN(spec, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}
