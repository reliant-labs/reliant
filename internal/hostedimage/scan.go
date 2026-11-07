package hostedimage

// Scanning a deploy/kcl tree for hosted images, without rendering it.
//
// WHY TEXT AND NOT A RENDER. `forge lint` judges a whole project, including
// envs nobody is deploying, and rendering each one costs a KCL evaluation per
// env plus whatever that env's render needs to exist (a cluster context, a
// port allocation, a control-plane credential for its digests). A lint that
// could only run where a deploy could run would not be a lint. `forge env
// render <env>` DOES render, and it is the authoritative check; this is the
// cheap, offline approximation that catches the same mistake earlier.
//
// WHAT IT CANNOT SEE, STATED RATHER THAN HIDDEN. KCL is a program: an image
// composed at render time (`image = base + "/" + name`), or a runtime chosen
// by a conditional, is invisible to a text scan. So this scan UNDER-reports,
// never over-reports — it only ever names a literal string someone wrote
// beside a literal forge.OnHosted. A finding here is therefore always real;
// the absence of one is not a guarantee, which is exactly why the render-time
// check exists too.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// nameField and imageField are the two declarations this scan joins.
	// Anchored on the field so a name inside a command path or a comment
	// does not read as a declaration.
	nameField  = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"\s*$`)
	imageField = regexp.MustCompile(`(?m)^\s*image\s*=\s*"([^"]+)"\s*$`)
	// onHostedBinding is a runtime bound to the platform. The name it
	// applies to is the nearest enclosing `name = "..."`, or — for the
	// `wl.api | {runtime = forge.OnHosted {}}` override shape — the
	// reference on its left.
	onHostedBinding = regexp.MustCompile(`forge\.OnHosted\s*\{`)
	// overrideTarget matches `<ident>.<name> | {` and `<name> | {`: an env
	// binding a workload declared elsewhere.
	overrideTarget = regexp.MustCompile(`(?m)([A-Za-z_][\w]*)\s*\|\s*\{`)
)

// ScanTree returns every hosted item it can see in the deploy KCL tree at
// dir, as (owner, image) pairs ready for OffBase.
//
// It joins two facts that are frequently declared in different files: an
// image on a workload in `workloads.k`, and that workload's runtime binding
// in `deploy/kcl/<env>/main.k`. Reporting only same-literal declarations
// would miss the shape most real projects use.
//
// A missing or unreadable tree yields no items and no error: a project
// without a deploy tree has nothing to judge, and a lint must not fail over
// a file it merely hoped to find.
func ScanTree(dir string) []Item {
	images := map[string]string{}
	hosted := map[string]bool{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".k") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		scanSource(string(src), images, hosted)
		return nil
	})
	var out []Item
	for name := range hosted {
		if image := images[name]; image != "" {
			out = append(out, Item{Owner: name, Image: image})
		}
	}
	// Stable order: a lint's output must not reorder between runs.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Owner < out[j-1].Owner; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func scanSource(src string, images map[string]string, hosted map[string]bool) {
	src = stripComments(src)
	// name → image, from any literal declaring both.
	for _, loc := range nameField.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		if image := nearestImage(src, loc[0]); image != "" {
			images[name] = image
		}
	}
	// Names bound to OnHosted: the nearest enclosing name, or an override's
	// target.
	for _, loc := range onHostedBinding.FindAllStringIndex(src, -1) {
		if name := enclosingName(src, loc[0]); name != "" {
			hosted[name] = true
		}
		if name := overrideName(src, loc[0]); name != "" {
			hosted[name] = true
		}
	}
}

// nearestImage is the `image = "..."` nearest to a name declaration, searched
// within the same brace block. A literal declaring a name and no image
// contributes nothing.
func nearestImage(src string, nameAt int) string {
	start, end := blockBounds(src, nameAt)
	if m := imageField.FindStringSubmatch(src[start:end]); m != nil {
		return m[1]
	}
	return ""
}

// enclosingName is the `name = "..."` in the block containing at.
func enclosingName(src string, at int) string {
	start, end := blockBounds(src, at)
	if m := nameField.FindStringSubmatch(src[start:end]); m != nil {
		return m[1]
	}
	return ""
}

// overrideName reads the `wl.api | {` shape: an env rebinding a workload
// declared in another file. The name is the reference's LAST segment, which
// is the convention the scaffold and every project here follow
// (`wl.api | {runtime = forge.OnHosted {}}` binds the workload named "api").
func overrideName(src string, at int) string {
	start, _ := blockBounds(src, at)
	head := src[maxInt(0, start-200):start]
	matches := overrideTarget.FindAllStringSubmatch(head, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// blockBounds is the innermost brace block containing at.
func blockBounds(src string, at int) (int, int) {
	start := 0
	depth := 0
	for i := at; i >= 0; i-- {
		if src[i] == '}' {
			depth++
		} else if src[i] == '{' {
			if depth == 0 {
				start = i + 1
				break
			}
			depth--
		}
	}
	end := len(src)
	depth = 0
	for i := start; i < len(src); i++ {
		if src[i] == '{' {
			depth++
		} else if src[i] == '}' {
			if depth == 0 {
				end = i
				break
			}
			depth--
		}
	}
	return start, end
}

// stripComments removes `#` line comments and `"""` doc blocks. Both carry
// worked examples in the scaffolded files, and an example is not a
// declaration.
func stripComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inDoc := false
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, `"""`) {
			// A one-line docstring opens and closes on the same line.
			if strings.Count(line, `"""`) == 1 {
				inDoc = !inDoc
			}
			continue
		}
		if inDoc {
			continue
		}
		if i := commentStart(line); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// commentStart is the index of a `#` outside a string literal, or -1.
func commentStart(line string) int {
	inString := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '"':
			inString = !inString
		case '#':
			if !inString {
				return i
			}
		}
	}
	return -1
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
