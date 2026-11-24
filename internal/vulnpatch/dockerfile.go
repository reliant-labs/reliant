package vulnpatch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The Dockerfiles of the tree, read for their FROM lines: an OS package
// goes to the final stage's base, and the Go standard library to the golang
// builder stages. forge reads only FROM and never interprets the rest of a
// Dockerfile; building one is docker's job (Verify).

// dockerfile is one Dockerfile of the tree.
type dockerfile struct {
	rel    string // relative to the tree
	abs    string
	stages []*stage
}

// stage is one FROM instruction.
type stage struct {
	line  int    // 1-based line the image token is on
	image string // the image as written
	name  string // the stage's AS name, lowercased; "" when unnamed
	// variable is the part of the FROM that expands a variable and makes
	// the base it starts from unknown: the image (FROM ${BASE},
	// alpine:${V}, alpine@${DIGEST}) or a flag other than --platform. ""
	// when none does.
	variable string
	// platform is the literal --platform the FROM pins (linux/arm64), ""
	// when it pins none or names it through a variable.
	platform string
	// platformVariable is a --platform named through a variable
	// (--platform=$BUILDPLATFORM): it picks an architecture of the literal
	// base, not another base, so the stage is not opaque, but which
	// architecture is unknown.
	platformVariable string
}

// opaque reports a FROM whose base depends on a build argument: one in its
// image, or in a flag other than --platform.
func (s *stage) opaque() bool { return s.variable != "" }

// imageVariable reports a FROM whose image itself is named through a
// variable: which image, and so which release, is unknown.
func (s *stage) imageVariable() bool { return strings.Contains(s.image, "$") }

// isDockerfileName reports the names docker and buildah build by default or
// by convention: Dockerfile, Containerfile, Dockerfile.<x>, <x>.Dockerfile.
func isDockerfileName(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case lower == "dockerfile", lower == "containerfile":
		return true
	case strings.HasPrefix(lower, "dockerfile."), strings.HasSuffix(lower, ".dockerfile"):
		return !strings.HasSuffix(lower, ".dockerignore")
	}
	return false
}

func readDockerfile(root, abs string) (*dockerfile, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return parseDockerfile(relTo(root, abs), abs, data), nil
}

// parseDockerfile reads the FROM instructions of a Dockerfile: their image,
// the line it is on, and the stage name. Continuation lines (the escape
// character, `\` unless an `# escape=` directive says otherwise) are joined
// and comment lines dropped, as docker does.
func parseDockerfile(rel, abs string, data []byte) *dockerfile {
	d := &dockerfile{rel: rel, abs: abs}
	lines := strings.Split(string(data), "\n")
	escape := byte('\\')
	directives := true
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(line)
		if directives {
			if v, ok := parserDirective(trimmed, "escape"); ok && (v == "`" || v == `\`) {
				escape = v[0]
				continue
			}
			if !strings.HasPrefix(trimmed, "#") || trimmed == "" {
				directives = false
			}
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		start := i
		physical := []int{i}
		logical := trimmed
		for strings.HasSuffix(logical, string(escape)) && i+1 < len(lines) {
			logical = strings.TrimSuffix(logical, string(escape))
			i++
			next := strings.TrimSpace(strings.TrimRight(lines[i], "\r"))
			if strings.HasPrefix(next, "#") {
				continue
			}
			physical = append(physical, i)
			logical += " " + next
		}
		fields := strings.Fields(logical)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		args := fields[1:]
		var flags []string
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			flags, args = append(flags, args[0]), args[1:]
		}
		if len(args) == 0 {
			continue
		}
		st := &stage{image: args[0], line: start + 1}
		if strings.Contains(st.image, "$") {
			st.variable = st.image
		}
		for _, f := range flags {
			v, isPlatform := strings.CutPrefix(f, "--platform=")
			switch {
			case isPlatform && strings.Contains(v, "$"):
				st.platformVariable = f
			case isPlatform:
				st.platform = strings.ToLower(v)
			case strings.Contains(f, "$") && st.variable == "":
				st.variable = f // conservatively: forge does not know what it changes
			}
		}
		if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
			st.name = strings.ToLower(args[2])
		}
		// The image token's own line, for the edit.
		for _, n := range physical {
			if tokenIndex(strings.TrimRight(lines[n], "\r"), st.image) >= 0 {
				st.line = n + 1
				break
			}
		}
		d.stages = append(d.stages, st)
	}
	return d
}

// parserDirective reads `# key=value` at the top of a Dockerfile.
func parserDirective(line, key string) (string, bool) {
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "#")), "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(k), key) {
		return "", false
	}
	return strings.TrimSpace(v), true
}

// tokenIndex is where tok stands alone (between blanks or line ends) in
// line, after the FROM keyword; -1 when it does not.
func tokenIndex(line, tok string) int {
	from := 0
	if fields := strings.Fields(line); len(fields) > 0 && strings.EqualFold(fields[0], "FROM") {
		from = strings.Index(strings.ToUpper(line), "FROM") + len("FROM")
	}
	for i := from; i <= len(line)-len(tok); {
		j := strings.Index(line[i:], tok)
		if j < 0 {
			return -1
		}
		at := i + j
		end := at + len(tok)
		if (at == 0 || isBlank(line[at-1])) && (end == len(line) || isBlank(line[end]) || strings.IndexByte("\r\\`", line[end]) >= 0) {
			return at
		}
		i = at + 1
	}
	return -1
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// stageNamed is the index of the earlier stage a FROM names, or -1 when it
// names an image.
func (d *dockerfile) stageNamed(name string, before int) int {
	for i := before - 1; i >= 0; i-- {
		if d.stages[i].name != "" && strings.EqualFold(d.stages[i].name, name) {
			return i
		}
	}
	return -1
}

// baseOf follows stage aliases from the stage at index i to the FROM that
// names an image: the base its filesystem starts from.
func (d *dockerfile) baseOf(i int) *stage {
	for {
		st := d.stages[i]
		j := d.stageNamed(st.image, i)
		if st.opaque() || j < 0 {
			return st
		}
		i = j // always earlier, so this ends
	}
}

// finalPlatforms are the literal platforms the FROMs from the last stage to
// its base pin, each once, and the first --platform named through a
// variable on the way ("" when none is).
func (d *dockerfile) finalPlatforms() (literal []string, variable string) {
	for i := len(d.stages) - 1; i >= 0; {
		st := d.stages[i]
		if st.platform != "" {
			literal = appendUnique(literal, st.platform)
		}
		if variable == "" {
			variable = st.platformVariable
		}
		j := d.stageNamed(st.image, i)
		if st.opaque() || j < 0 {
			break
		}
		i = j
	}
	return literal, variable
}

// finalBase is the base image of the image the Dockerfile builds (its last
// stage), with why it cannot be refreshed when it cannot: nil and "" for a
// final stage FROM scratch, which carries no OS packages.
func (d *dockerfile) finalBase() (*stage, string) {
	if len(d.stages) == 0 {
		return nil, d.rel + " has no FROM"
	}
	st := d.baseOf(len(d.stages) - 1)
	switch {
	case st.imageVariable():
		return st, fmt.Sprintf("%s builds its final stage FROM %s: a base named by a build argument is left to the agent", d.rel, st.image)
	case st.opaque():
		return st, fmt.Sprintf("%s builds its final stage FROM %s %s: a base whose FROM flag expands a build argument is left to the agent", d.rel, st.variable, st.image)
	case strings.EqualFold(st.image, "scratch"):
		return nil, ""
	}
	return st, ""
}

// editFrom rewrites the image of the FROM on line from old to new, and
// nothing else of the file.
func editFrom(path string, line int, old, new string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := bytes.Split(data, []byte("\n"))
	if line < 1 || line > len(lines) {
		return fmt.Errorf("%s has no line %d", filepath.Base(path), line)
	}
	l := string(lines[line-1])
	at := tokenIndex(l, old)
	if at < 0 {
		return fmt.Errorf("%s:%d no longer names %s", filepath.Base(path), line, old)
	}
	lines[line-1] = []byte(l[:at] + new + l[at+len(old):])
	// atomicfile keeps the existing file's permission bits.
	return atomicfile.WriteFile(path, bytes.Join(lines, []byte("\n")), 0o644)
}

// imageRef is an image reference as a FROM line writes it:
// [registry/]repository[:tag][@digest].
type imageRef struct {
	name   string // as written, without tag or digest
	host   string // the registry: docker.io when the name has none
	path   string // the repository within it: library/alpine for alpine
	tag    string
	digest string
}

func parseImageRef(s string) (imageRef, error) {
	var r imageRef
	rest := s
	if name, digest, ok := strings.Cut(rest, "@"); ok {
		rest, r.digest = name, digest
		if !strings.HasPrefix(r.digest, "sha256:") {
			return r, fmt.Errorf("%q: digest %q is not sha256", s, r.digest)
		}
	}
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		rest, r.tag = rest[:i], rest[i+1:]
	}
	if rest == "" || strings.ContainsAny(rest, " $") {
		return r, fmt.Errorf("%q is not an image reference", s)
	}
	r.name = rest
	first, remainder, hasSlash := strings.Cut(rest, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.host, r.path = first, remainder
	} else {
		r.host, r.path = "docker.io", rest
		if !hasSlash {
			r.path = "library/" + rest
		}
	}
	r.path = strings.ToLower(r.path)
	return r, nil
}

// repository is host/path, the registry API's address of the repository.
func (r imageRef) repository() string { return r.host + "/" + r.path }

// String spells the reference the way it was written, with its tag and
// digest.
func (r imageRef) String() string {
	s := r.name
	if r.tag != "" {
		s += ":" + r.tag
	}
	if r.digest != "" {
		s += "@" + r.digest
	}
	return s
}

// short is the repository's last path element: alpine, golang, static-debian12.
func (r imageRef) short() string { return r.path[strings.LastIndex(r.path, "/")+1:] }

// applyDockerfile rewrites the FROM lines of one Dockerfile's bumps.
func applyDockerfile(bs []*plannedBump) ([]string, error) {
	edits := []string{}
	for _, b := range bs {
		path := filepath.Join(b.dir, filepath.Base(b.manifest))
		if err := editFrom(path, b.line, b.from, b.to); err != nil {
			return edits, err
		}
		edits = append(edits, fmt.Sprintf("%s:%d FROM %s → %s", b.manifest, b.line, b.from, b.to))
	}
	return edits, nil
}
