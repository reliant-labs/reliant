package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/templates"
)

// The air step of `forge fix trimpath-default` (upgrade_trimpath_default.go):
// $FORGE_GO_BUILD_FLAGS in the build commands of the project's air configs.
//
// forge env up hands air a GoBuild's flags (-trimpath among them) in
// $FORGE_GO_BUILD_FLAGS, and only a build command that expands it uses them.
// A .air.toml scaffolded before v1.11.0's predecessor added the variable
// builds without:
//
//   - [build] cmd: `$FORGE_GO_BUILD_FLAGS ` goes in right after `go build `,
//     as the current template spells it;
//   - [build.windows] cmd: replaced by the current template's PowerShell form
//     (which splits the variable into arguments) when it is byte for byte an
//     older forge's form for the same binary; anything else is the manual step.
//
// It reads .air.toml and every air_config an OnHost runtime under deploy/kcl
// names, never a debug config (.air-debug.toml keeps -trimpath=false on
// purpose), and never a command that already says trimpath. The result must
// decode as TOML to the old document with only those commands changed.

// trimpathAirDefault is the air config `runner = "air"` reads without one.
const trimpathAirDefault = ".air.toml"

var (
	kclAirConfigRe = regexp.MustCompile(`air_config[ \t]*=[ \t]*"([^"\n]+)"`)
	// airSectionRe is a TOML table header line.
	airSectionRe = regexp.MustCompile(`^[ \t]*\[[ \t]*([A-Za-z0-9_.\- \t"]+?)[ \t]*\][ \t]*(?:#.*)?$`)
	// airCmdRe is a `cmd = "…"` or `cmd = '…'` line: indent, quote, value.
	airCmdRe = regexp.MustCompile(`^([ \t]*cmd[ \t]*=[ \t]*)(["'])(.*)(["'])([ \t]*(?:#.*)?)$`)
	// airWindowsOldRe is [build.windows] cmd as forge scaffolded it from its
	// first Windows support until $FORGE_GO_BUILD_FLAGS (#2590).
	airWindowsOldRe = regexp.MustCompile(`^forge storage check \./tmp; if \(\$LASTEXITCODE -ne 0\) \{ exit \$LASTEXITCODE \}; \$env:CGO_ENABLED=0; go build -buildvcs=false -o \./tmp/([A-Za-z0-9_.\-]+)\.exe \./cmd/([A-Za-z0-9_.\-]+)$`)
)

// trimpathAirConfigs are the air configs to read: .air.toml and each
// air_config the KCL names, project-relative, never a debug config.
func trimpathAirConfigs(dir string) ([]string, error) {
	seen := map[string]bool{trimpathAirDefault: true}
	files, err := trimpathKCLFiles(dir)
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) //nolint:gosec // the project's own KCL
		if err != nil {
			return nil, err
		}
		for _, m := range kclAirConfigRe.FindAllStringSubmatch(string(b), -1) {
			p := path.Clean(filepath.ToSlash(m[1]))
			if path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
				continue
			}
			seen[p] = true
		}
	}
	var out []string
	for p := range seen {
		if strings.Contains(path.Base(p), "debug") {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func trimpathAirStep(dir string, write bool) (codemod.Report, error) {
	var rep codemod.Report
	configs, err := trimpathAirConfigs(dir)
	if err != nil {
		return rep, err
	}
	for _, rel := range configs {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		b, err := os.ReadFile(p) //nolint:gosec // the project's own air config
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return rep, err
		}
		out, rewrites, next := planAirTrimpath(rel, string(b))
		rep.Next = append(rep.Next, next...)
		if out == string(b) {
			continue
		}
		if write {
			if err := writeKeepingMode(p, []byte(out)); err != nil {
				return rep, err
			}
		}
		rep.Rewrites = append(rep.Rewrites, rewrites...)
		rep.Verify = append(rep.Verify, Name()+" env up dev (air builds with the env's GoBuild flags)")
	}
	return rep, nil
}

// planAirTrimpath is one air config's edit: the new content (src when
// nothing changes), the report lines, and the manual steps.
func planAirTrimpath(rel, src string) (string, []string, []string) {
	var before map[string]any
	if _, err := toml.Decode(src, &before); err != nil {
		if strings.Contains(src, "go build") && !strings.Contains(src, airFlagsVar) {
			return src, nil, []string{fmt.Sprintf("%s does not parse as TOML (%v), so it was left alone. Put `$%s ` right after `go build ` in its [build] cmd by hand", rel, err, airFlagsVar)}
		}
		return src, nil, nil
	}
	lines := strings.SplitAfter(src, "\n")
	section := ""
	var rewrites, next []string
	changed := map[string]bool{}
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		if m := airSectionRe.FindStringSubmatch(body); m != nil {
			section = strings.Join(strings.Fields(m[1]), "")
			continue
		}
		if section != "build" && section != "build.windows" {
			continue
		}
		m := airCmdRe.FindStringSubmatch(body)
		if len(m) != 6 || m[2] != m[4] {
			continue
		}
		value := m[3]
		if !strings.Contains(value, "go build") || strings.Contains(value, airFlagsVar) || strings.Contains(value, "trimpath") {
			continue
		}
		lineNo := i + 1
		var replacement string
		switch section {
		case "build":
			idx := strings.Index(value, "go build ")
			if idx < 0 {
				next = append(next, fmt.Sprintf("%s:%d: [build] cmd runs `go build` in a form forge does not edit, so it was left alone. Pass $%s to that go build by hand", rel, lineNo, airFlagsVar))
				continue
			}
			value = value[:idx] + "go build $" + airFlagsVar + " " + value[idx+len("go build "):]
			replacement = m[1] + m[2] + value + m[4] + m[5]
			rewrites = append(rewrites, fmt.Sprintf("%s:%d: [build] cmd passes $%s to go build (the env's GoBuild flags, -trimpath among them)", rel, lineNo, airFlagsVar))
		case "build.windows":
			cur, ok := airWindowsCurrentCmd(value)
			if !ok {
				next = append(next, fmt.Sprintf("%s:%d: [build.windows] cmd is not the form an older forge scaffolded, so it was left alone. "+
					"Split $env:%s into arguments and pass them to go build by hand, as forge's current form does: "+
					"`$f = @(\"$env:%s\" -split \" \" | Where-Object { $_ }); go build @f …`", rel, lineNo, airFlagsVar, airFlagsVar))
				continue
			}
			replacement = m[1] + "'" + cur + "'" + m[5]
			rewrites = append(rewrites, fmt.Sprintf("%s:%d: [build.windows] cmd is forge's current PowerShell form, which passes $env:%s to go build", rel, lineNo, airFlagsVar))
		}
		lines[i] = replacement + line[len(body):]
		changed[section] = true
	}
	if len(rewrites) == 0 {
		return src, nil, next
	}
	out := strings.Join(lines, "")
	if why := verifyAirTrimpath(before, out, changed); why != "" {
		return src, nil, append(next, fmt.Sprintf("%s: forge's edit did not verify (%s), so it was left alone. Put `$%s ` right after `go build ` in its [build] cmd by hand", rel, why, airFlagsVar))
	}
	return out, rewrites, next
}

// airFlagsVar is the variable forge env up hands air a GoBuild's flags in
// (hostlaunch.GoBuildFlagsEnv; spelled here so this step reads no runtime).
const airFlagsVar = "FORGE_GO_BUILD_FLAGS"

// airWindowsCurrentCmd is the current template's [build.windows] cmd for the
// binary an older forge's form builds; false when cmd is not that form.
func airWindowsCurrentCmd(cmd string) (string, bool) {
	m := airWindowsOldRe.FindStringSubmatch(cmd)
	if len(m) != 3 || m[1] != m[2] {
		return "", false
	}
	rendered, err := templates.ProjectTemplates().Render("air.toml.tmpl", map[string]any{"Name": m[1]})
	if err != nil {
		return "", false
	}
	section := ""
	for _, line := range strings.Split(string(rendered), "\n") {
		if s := airSectionRe.FindStringSubmatch(line); s != nil {
			section = strings.Join(strings.Fields(s[1]), "")
			continue
		}
		if section != "build.windows" {
			continue
		}
		if c := airCmdRe.FindStringSubmatch(line); len(c) == 6 && c[2] == "'" && c[4] == "'" && strings.Contains(c[3], airFlagsVar) {
			return c[3], true
		}
	}
	return "", false
}

// verifyAirTrimpath proves an edit: the result decodes, and differs from
// before only in the cmd of the sections it changed, each of which now
// names $FORGE_GO_BUILD_FLAGS.
func verifyAirTrimpath(before map[string]any, out string, changed map[string]bool) string {
	var after map[string]any
	if _, err := toml.Decode(out, &after); err != nil {
		return fmt.Sprintf("the result does not parse: %v", err)
	}
	strip := func(doc map[string]any) {
		build, _ := doc["build"].(map[string]any)
		if build == nil {
			return
		}
		if changed["build"] {
			delete(build, "cmd")
		}
		if win, _ := build["windows"].(map[string]any); win != nil && changed["build.windows"] {
			delete(win, "cmd")
		}
	}
	build, _ := after["build"].(map[string]any)
	if changed["build"] {
		if cmd, _ := build["cmd"].(string); !strings.Contains(cmd, "$"+airFlagsVar) {
			return "[build] cmd does not name $" + airFlagsVar + " afterwards"
		}
	}
	if changed["build.windows"] {
		win, _ := build["windows"].(map[string]any)
		if cmd, _ := win["cmd"].(string); !strings.Contains(cmd, airFlagsVar) {
			return "[build.windows] cmd does not name " + airFlagsVar + " afterwards"
		}
	}
	strip(before)
	strip(after)
	if !reflect.DeepEqual(before, after) {
		return "the rest of the file decodes differently afterwards"
	}
	return ""
}
