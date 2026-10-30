// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	forgecli "github.com/reliant-labs/forge/cli"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

// =============================================================================
// THE PINNED FORGE'S SKILLS
//
// forge's shipped skills live inside the forge binary, so the skills a forge
// serves describe THAT forge: its commands, flags, generated layout and
// conventions. The forge compiled into reliant is whatever reliant was built
// with; every project pins its own (forge.yaml forge_version). Reading the
// shipped skills from the embedded forge handed a chat in a project pinned to
// v1.0.4 the v0.1.47 guidance — the same skew forge report 86c6fd64 found in
// `reliant forge generate`, one layer up.
//
// So inside a project that pins another forge, the shipped skills come from
// THAT forge: `forge skill list --json` for the catalog and `forge skill load
// <path>` for each body, run by the binary forge's own hand-off resolves
// (forgecli.ResolvePinnedForge: the project's .tools/bin/forge, the pinned
// build cache, or a one-time build). Both outputs are a pure function of the
// forge version for shipped skills, so they are cached per pinned forge, in
// memory and — for a module version, which never changes — on disk.
//
// What stays on the embedded forge: user (~/.forge/skills) and project
// (.forge/skills) skills, which are the user's files rather than any forge's
// content, and every project with no pin.
//
// When the pinned forge cannot be obtained (offline, an unpublished pin),
// substituting the embedded forge's framework skills is the bug. Instead the
// forge namespace is one entry that says what failed and how to fix it; the
// project-agnostic methodology skills (emit: general) keep coming from the
// embedded forge, so presets that preload them still start — a chat must
// never fail to start over this.
// =============================================================================

// pinnedSkillTimeout bounds obtaining the pinned forge during discovery,
// which may build it once (about a minute).
const pinnedSkillTimeout = 4 * time.Minute

// Seams, swapped by the tests.
var (
	// resolvePinnedForge is forge's own decision of which forge a command
	// runs under in a project.
	resolvePinnedForge = forgecli.ResolvePinnedForge
	// runPinnedForge runs the pinned forge bin with args in dir.
	runPinnedForge = execPinnedForge
	// pinnedSkillCacheDir is where a module version's skills are kept.
	pinnedSkillCacheDir = defaultPinnedSkillCacheDir
)

// pinnedForgeCache holds what a pinned forge printed, per forge identity.
var pinnedForgeCache sync.Map // key -> *pinnedForgeEntry

type pinnedForgeEntry struct {
	mu     sync.Mutex
	list   []forgecli.Skill
	bodies map[string]string
}

// pinnedResolutionTTL is how long one answer to "which forge does this project
// pin" is reused. Resolving can run the project's .tools/bin/forge to read its
// version, and a discovery that loads every body asks once per skill.
const pinnedResolutionTTL = time.Minute

type pinnedResolution struct {
	yamlKey string
	at      time.Time
	pf      forgecli.PinnedForge
	ok      bool
	err     error
}

var pinnedResolutions sync.Map // baseDir -> pinnedResolution

// pinnedForgeFor reports the forge the project at baseDir pins when it is not
// the embedded one (ok), or why it could not be obtained (err). An answer is
// reused for pinnedResolutionTTL while forge.yaml is unchanged — a failure
// too, so an unobtainable pin is not rebuilt once per skill.
func pinnedForgeFor(baseDir string) (forgecli.PinnedForge, bool, error) {
	yaml, _ := os.ReadFile(filepath.Join(baseDir, "forge.yaml"))
	sum := sha256.Sum256(yaml)
	key := hex.EncodeToString(sum[:])
	if v, ok := pinnedResolutions.Load(baseDir); ok {
		r := v.(pinnedResolution)
		if r.yamlKey == key && time.Since(r.at) < pinnedResolutionTTL {
			return r.pf, r.ok, r.err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pinnedSkillTimeout)
	defer cancel()
	pf, ok, err := resolvePinnedForge(ctx, baseDir, "skill load", io.Discard)
	pinnedResolutions.Store(baseDir, pinnedResolution{yamlKey: key, at: time.Now(), pf: pf, ok: ok, err: err})
	return pf, ok, err
}

func pinnedEntry(key string) *pinnedForgeEntry {
	e, _ := pinnedForgeCache.LoadOrStore(key, &pinnedForgeEntry{bodies: map[string]string{}})
	return e.(*pinnedForgeEntry)
}

// pinnedShippedSkills is the pinned forge's own (scope "forge") skills.
func pinnedShippedSkills(pf forgecli.PinnedForge, baseDir string) ([]forgecli.Skill, error) {
	e := pinnedEntry(pf.Key)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadList(pf, baseDir)
}

// loadList reads the pinned forge's catalog once: `skill list --json
// --with-bodies`, which carries every body in one call, or — from a forge that
// predates the flag — the bare listing, the bodies then loaded one by one.
// e.mu is held.
func (e *pinnedForgeEntry) loadList(pf forgecli.PinnedForge, baseDir string) ([]forgecli.Skill, error) {
	if e.list != nil {
		return e.list, nil
	}
	raw, err := cachedPinnedOutput(pf, "list.json", func() ([]byte, error) {
		out, err := runPinnedForge(pf.Path, baseDir, "skill", "list", "--json", "--with-bodies")
		if err != nil {
			out, err = runPinnedForge(pf.Path, baseDir, "skill", "list", "--json")
		}
		return out, err
	})
	if err != nil {
		return nil, err
	}
	var listed []struct {
		Path        string  `json:"path"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Scope       string  `json:"scope"`
		Emit        string  `json:"emit"`
		Relevance   string  `json:"relevance"`
		Body        *string `json:"body"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, fmt.Errorf("%s: `skill list --json`: %w", pf.Want, err)
	}
	skills := make([]forgecli.Skill, 0, len(listed))
	for _, s := range listed {
		if s.Scope != "forge" {
			continue // the user's files: read where they are, never cached
		}
		skills = append(skills, forgecli.Skill{Path: s.Path, Name: s.Name, Description: s.Description, Scope: s.Scope, Emit: s.Emit, Relevance: s.Relevance})
		if s.Body != nil {
			e.bodies[s.Path] = *s.Body
		}
	}
	e.list = skills
	return skills, nil
}

// pinnedSkillBody is `skill load <path>` as the pinned forge prints it.
func pinnedSkillBody(pf forgecli.PinnedForge, baseDir, path string) (string, error) {
	e := pinnedEntry(pf.Key)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.loadList(pf, baseDir); err != nil {
		return "", err
	}
	if body, ok := e.bodies[path]; ok {
		return body, nil
	}
	sum := sha256.Sum256([]byte(path))
	raw, err := cachedPinnedOutput(pf, "body-"+hex.EncodeToString(sum[:12])+".md", func() ([]byte, error) {
		return runPinnedForge(pf.Path, baseDir, "skill", "load", path)
	})
	if err != nil {
		return "", err
	}
	e.bodies[path] = string(raw)
	return string(raw), nil
}

// cachedPinnedOutput returns the file name under pf's cache entry, producing
// and keeping it with produce when absent. Only a module version is kept on
// disk: a linked checkout's key moves with every edit.
func cachedPinnedOutput(pf forgecli.PinnedForge, name string, produce func() ([]byte, error)) ([]byte, error) {
	dir := ""
	if strings.HasPrefix(pf.Key, "pin:") {
		if base := pinnedSkillCacheDir(); base != "" {
			sum := sha256.Sum256([]byte(pf.Key))
			dir = filepath.Join(base, hex.EncodeToString(sum[:12]))
		}
	}
	if dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return b, nil
		}
	}
	b, err := produce()
	if err != nil {
		return nil, err
	}
	if dir != "" && os.MkdirAll(dir, 0o755) == nil {
		// Written aside and renamed: a reader never sees half a file.
		if tmp, err := os.CreateTemp(dir, "."+name+"-"); err == nil {
			_, werr := tmp.Write(b)
			cerr := tmp.Close()
			if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(dir, name)) != nil {
				_ = os.Remove(tmp.Name())
			}
		}
	}
	return b, nil
}

func defaultPinnedSkillCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "reliant", "forge-skills")
}

// execPinnedForge runs the pinned forge. Its argv[0] is the name an agent
// types, so command references in a body read `reliant forge …` exactly as the
// embedded forge renders them (forge names itself from argv[0]); with the
// hand-off, that command runs the pinned forge too.
func execPinnedForge(bin, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Args[0] = reliantForgeName()
	cmd.Dir = dir
	var env []string
	for _, kv := range os.Environ() {
		// No inherited hand-off marker: a forge older than the
		// single-process marker would believe one.
		if !strings.HasPrefix(kv, "FORGE_VERSION_DELEGATED=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("forge %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// reliantForgeName is how forge names itself when embedded here.
func reliantForgeName() string {
	return filepath.Base(os.Args[0]) + " forge"
}

// shippedSkillsFor returns the forge-shipped skills baseDir's agents should
// read, plus the user's own (user scope) from the embedded reader. In a
// project pinned to another forge the shipped ones are that forge's; when it
// cannot be obtained, unavailable carries the reason and only the embedded
// general-audience skills are returned.
//
// includeProject keeps the project's own (.forge/skills) skills in the result.
func shippedSkillsFor(baseDir string, hasForgeYAML, includeProject bool) (skills []forgecli.Skill, unavailable error) {
	embedded, err := forgecli.ListSkills(baseDir)
	if err != nil {
		return nil, nil
	}
	local := func(s forgecli.Skill) bool { return s.Scope != projectSkillScope || includeProject }
	if !hasForgeYAML {
		return keepSkills(embedded, local), nil
	}
	pf, ok, err := pinnedForgeFor(baseDir)
	if !ok && err == nil {
		return keepSkills(embedded, local), nil
	}
	if err == nil {
		var pinned []forgecli.Skill
		if pinned, err = pinnedShippedSkills(pf, baseDir); err == nil {
			// The user's own skills shadow a shipped one of the same path,
			// as they do in forge's own listing.
			shadow := map[string]bool{}
			var out []forgecli.Skill
			for _, s := range embedded {
				if s.Scope != "forge" {
					shadow[s.Path] = true
					if local(s) {
						out = append(out, s)
					}
				}
			}
			for _, s := range pinned {
				if !shadow[s.Path] {
					out = append(out, s)
				}
			}
			return out, nil
		}
	}
	var general []forgecli.Skill
	for _, s := range embedded {
		if local(s) && (s.Scope != "forge" || s.Emit == "general") {
			general = append(general, s)
		}
	}
	return general, err
}

func keepSkills(skills []forgecli.Skill, keep func(forgecli.Skill) bool) []forgecli.Skill {
	out := skills[:0:0]
	for _, s := range skills {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

// pinnedForgeUnavailable is the forge namespace when the project's forge
// cannot be obtained: what failed, and that nothing else will stand in for it.
func pinnedForgeUnavailable(baseDir, source string, cause error) Definition {
	body := "# Forge skills are unavailable\n\n" +
		"This project pins a forge that could not be obtained, so its skills cannot be shown — " +
		"and the forge built into reliant is a different version, whose guidance would be wrong here.\n\n" +
		"```\n" + strings.TrimSpace(cause.Error()) + "\n```\n\n" +
		"Fix what stopped it (offline? a pin nobody published?), or move the pin deliberately with `" +
		reliantForgeName() + " project upgrade`. Every `" + reliantForgeName() + "` command in this project " +
		"fails the same way until then.\n"
	return Definition{
		Name:        forgeNamespace,
		Description: forgePurpose + " Unavailable: this project's pinned forge could not be obtained.",
		Body:        body,
		Path:        forgeSyntheticPath(baseDir, ""),
		Scope:       skillscore.ScopeForge,
		Format:      skillscore.SkillFormatClaudeMarkdown,
		SkillDir:    forgeSyntheticPath(baseDir, ""),
		SkillPath:   forgeNamespace,
		Source:      source,
	}
}

// pinnedBodyFor is the body of the forge skill at forgePath in baseDir when the
// project pins another forge and the skill is that forge's (not one of the
// user's, which shadow it): handled reports whether it applies.
func pinnedBodyFor(baseDir, forgePath string) (body string, handled bool, err error) {
	if _, err := os.Stat(filepath.Join(baseDir, "forge.yaml")); err != nil {
		return "", false, nil
	}
	if embedded, err := forgecli.ListSkills(baseDir); err == nil {
		for _, s := range embedded {
			if s.Path == forgePath && s.Scope != "forge" {
				return "", false, nil // the user's file
			}
		}
	}
	pf, ok, err := pinnedForgeFor(baseDir)
	if err != nil {
		return "", true, err
	}
	if !ok {
		return "", false, nil
	}
	body, err = pinnedSkillBody(pf, baseDir, forgePath)
	return body, true, err
}
