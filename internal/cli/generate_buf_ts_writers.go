package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// generate_buf_ts_writers.go — one writer per TypeScript stub directory.
//
// A frontend's src/gen is generated code, and generated code has exactly one
// generator or it has none: two writers with different plugin versions or
// options overwrite each other's output on every run, and each run's diff is
// the other writer's work undone.
//
// That is what adopting reliant's web app produced. reliant generates its
// stubs from its own root buf.gen.yaml — protoc-gen-es pinned at v2.15.0 in
// tools/protoc-gen-es, `ts_nocheck=true` — while forge's TypeScript step
// wrote a frontends/web/buf.gen.yaml of its own and ran whatever
// protoc-gen-es the frontend's node_modules held (v2.10.2,
// `import_extension=.js`). 57 stubs churned, and forge's copies failed the
// app's `erasableSyntaxOnly` with 83 TS1294s. `make generate-all` and
// `forge generate` would have rewritten each other's output forever.
//
// So before forge writes a frontend's stubs it asks who already does:
//
//   - a root buf.gen*.yaml with a plugin whose `out:` is the frontend's
//     src/gen makes the stubs PROJECT-OWNED. The project's own plugin pin and
//     options are the generator, and forge writes no second copy. (buf.gen.yaml
//     itself is run by forge's Go-stubs step, so its output is current by the
//     time this step would have run.)
//   - that AND the frontend's own buf.gen.yaml targeting the same directory is
//     two writers, and generate refuses until one is removed.
//   - neither, but src/gen already holds stubs whose `@generated` header names
//     options other than forge's: someone else generates them from somewhere
//     forge cannot see (a Makefile, a template whose `out:` still names the
//     directory the frontend moved from). forge leaves them, says so, and
//     names them in the run's summary instead of becoming the second writer.

// tsStubWriter is one root buf template plugin that writes a frontend's
// src/gen.
type tsStubWriter struct {
	template string // project-relative template path ("buf.gen.yaml")
	plugin   string // the plugin as the template names it
	opts     []string
}

func (w tsStubWriter) String() string {
	s := w.template + " (" + w.plugin
	if len(w.opts) > 0 {
		s += ", opt " + strings.Join(w.opts, ",")
	}
	return s + ")"
}

// rootBufTemplates lists the buf generation templates at the project root:
// buf.gen.yaml and every buf.gen.<name>.yaml / buf.gen-<name>.yaml beside it,
// sorted.
func rootBufTemplates(projectDir string) []string {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "buf.gen") {
			continue
		}
		if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// projectTSStubWriters returns the root template plugins whose `out:` is
// genRel (project-relative, slash-separated).
func projectTSStubWriters(projectDir, genRel string) []tsStubWriter {
	want := path.Clean(genRel)
	var out []tsStubWriter
	for _, name := range rootBufTemplates(projectDir) {
		body, err := os.ReadFile(filepath.Join(projectDir, name))
		if err != nil {
			continue
		}
		var tmpl struct {
			Plugins []struct {
				Local  any    `yaml:"local"`
				Remote string `yaml:"remote"`
				Plugin string `yaml:"plugin"` // v1
				Name   string `yaml:"name"`   // v1
				Out    string `yaml:"out"`
				Opt    any    `yaml:"opt"`
			} `yaml:"plugins"`
		}
		if yaml.Unmarshal(body, &tmpl) != nil {
			continue
		}
		for _, p := range tmpl.Plugins {
			if p.Out == "" || path.Clean(strings.TrimPrefix(filepath.ToSlash(p.Out), "./")) != want {
				continue
			}
			plugin := strings.Join(stringArgv(p.Local), " ")
			for _, alt := range []string{p.Remote, p.Plugin, p.Name} {
				if plugin == "" {
					plugin = alt
				}
			}
			out = append(out, tsStubWriter{template: name, plugin: plugin, opts: stringArgv(p.Opt)})
		}
	}
	return out
}

// staleTSStubOut is a root template plugin writing a src/gen that is not
// this frontend's but has its name — the `out:` a moved frontend left behind.
type staleTSStubOut struct{ template, out string }

// staleStubOuts finds root template plugins whose out is <x>/<name>/src/gen
// or <name>/src/gen for this frontend's directory name, at another path:
// reliant's buf.gen.yaml still wrote web/src/gen after the app moved to
// frontends/web.
func staleStubOuts(projectDir, feSlash string) []staleTSStubOut {
	name := path.Base(feSlash)
	var out []staleTSStubOut
	for _, tmplName := range rootBufTemplates(projectDir) {
		body, err := os.ReadFile(filepath.Join(projectDir, tmplName))
		if err != nil {
			continue
		}
		var tmpl struct {
			Plugins []struct {
				Out string `yaml:"out"`
			} `yaml:"plugins"`
		}
		if yaml.Unmarshal(body, &tmpl) != nil {
			continue
		}
		for _, p := range tmpl.Plugins {
			o := path.Clean(strings.TrimPrefix(filepath.ToSlash(p.Out), "./"))
			if o != path.Join(feSlash, "src", "gen") && (o == path.Join(name, "src", "gen") || strings.HasSuffix(o, "/"+path.Join(name, "src", "gen"))) {
				out = append(out, staleTSStubOut{template: tmplName, out: o})
			}
		}
	}
	return out
}

// forgeTSStubParams is the parameter string protoc-gen-es stamps into every
// stub forge's own template generates (target=ts, import_extension=.js).
const forgeTSStubParams = "target=ts,import_extension=.js"

// esGeneratedHeaderRE matches the header protoc-gen-es writes at the top of
// every file: `// @generated by protoc-gen-es v2.15.0 with parameter "target=ts"`.
var esGeneratedHeaderRE = regexp.MustCompile(`@generated by (protoc-gen-es v\S+)(?: with parameter "([^"]*)")?`)

// foreignTSStubs reports the generators of the stubs already in absGenDir
// that were not generated with forge's options — each distinct plugin and
// parameter string, read from the `@generated` header of the *_pb.ts files,
// joined with "; ". "" means there are no stubs, no headers to read, or only
// forge's own options. Every file is consulted, not the first: an app's
// src/gen commonly holds two generators' output (reliant's main stubs and
// its vendored control-plane ones, at different plugin pins).
func foreignTSStubs(absGenDir string) string {
	seen := map[string]bool{}
	var found []string
	_ = filepath.WalkDir(absGenDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_pb.ts") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		head := make([]byte, 512)
		n, _ := f.Read(head)
		_ = f.Close()
		m := esGeneratedHeaderRE.FindSubmatch(head[:n])
		if len(m) < 3 || sameParams(string(m[2]), forgeTSStubParams) {
			return nil
		}
		gen := fmt.Sprintf("%s with parameter %q", m[1], string(m[2]))
		if !seen[gen] {
			seen[gen] = true
			found = append(found, gen)
		}
		return nil
	})
	sort.Strings(found)
	return strings.Join(found, "; ")
}

// sameParams compares two protoc plugin parameter strings as sets.
func sameParams(a, b string) bool {
	split := func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out
	}
	return strings.Join(split(a), ",") == strings.Join(split(b), ",")
}

// tsStubOwnership is the decision for one frontend's src/gen.
type tsStubOwnership int

const (
	// tsStubsForge: forge's per-frontend template writes them (the default).
	tsStubsForge tsStubOwnership = iota
	// tsStubsProject: a root template writes them; forge writes nothing.
	tsStubsProject
	// tsStubsForeign: stubs from an unknown generator are present; forge
	// leaves them and reports it.
	tsStubsForeign
)

// decideTSStubOwnership applies the one-writer rule to feDir (project-
// relative). It returns an error when two writers are declared.
func decideTSStubOwnership(projectDir, feName, feDir string) (tsStubOwnership, error) {
	feSlash := filepath.ToSlash(feDir)
	genRel := path.Join(feSlash, "src", "gen")
	writers := projectTSStubWriters(projectDir, genRel)
	feTemplate := path.Join(feSlash, "buf.gen.yaml")
	_, feTemplateErr := os.Stat(filepath.Join(projectDir, filepath.FromSlash(feTemplate)))
	hasFeTemplate := feTemplateErr == nil

	switch {
	case len(writers) > 1 || (len(writers) == 1 && hasFeTemplate):
		names := make([]string, 0, len(writers)+1)
		for _, w := range writers {
			names = append(names, w.String())
		}
		if hasFeTemplate {
			names = append(names, feTemplate+" (forge's per-frontend template)")
		}
		return 0, cliutil.UserErr(Name()+" generate (TypeScript stubs)",
			fmt.Sprintf("frontend %s: %d buf templates write %s — %s. Two generators for one directory "+
				"overwrite each other's output on every run, so no stubs were written", feName, len(names), genRel,
				strings.Join(names, "; ")),
			"",
			fmt.Sprintf("keep one writer: delete %s to let your own template own the stubs (forge then writes "+
				"none), or drop the plugin writing %s from your template to hand them to forge", feTemplate, genRel))
	case len(writers) == 1:
		w := writers[0]
		how := "run your generator to refresh them"
		if w.template == "buf.gen.yaml" {
			how = "this run's `buf generate` (the Go-stubs step) refreshed them"
		}
		fmt.Printf("  ℹ️  frontend %s: TypeScript stubs are project-owned — %s writes %s, so forge writes no second copy (%s)\n",
			feName, w, genRel, how)
		return tsStubsProject, nil
	case !hasFeTemplate:
		if gen := foreignTSStubs(filepath.Join(projectDir, filepath.FromSlash(genRel))); gen != "" {
			why := fmt.Sprintf("holds TypeScript stubs from %s, which no buf template in this project declares, so "+
				"forge did not become a second writer. Point the template that generates them at it (`out: %s` in "+
				"a root buf.gen*.yaml) to make them project-owned, or delete the directory to let forge generate them",
				gen, genRel)
			if stale := staleStubOuts(projectDir, feSlash); len(stale) > 0 {
				why += fmt.Sprintf(" — %s still writes %s: if the frontend moved here, that `out:` is the one to update",
					stale[0].template, stale[0].out)
			}
			checksums.NoteLeftAlone(genRel, why)
			fmt.Printf("  ⚠️  frontend %s: %s %s\n", feName, genRel, why)
			return tsStubsForeign, nil
		}
	}
	return tsStubsForge, nil
}
