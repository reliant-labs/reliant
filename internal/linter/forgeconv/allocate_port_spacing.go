// File: internal/linter/forgeconv/allocate_port_spacing.go
//
// The forgeconv-allocate-port-spacing analyzer catches a defect class in
// the parallel-dev-stack port allocator that is invisible until a
// project's stack ceiling grows: two DIFFERENT `allocate_port(base, key)`
// base literals that are congruent modulo BlockSize collide at whichever
// block their separation, in blocks, equals. `allocate_port` computes
// `base + block(key)*BlockSize` (see internal/kclplugin/resolver.go and
// internal/config.DevStackConfig); if base1 and base2 share a remainder
// mod BlockSize, then at block N = (base2-base1)/BlockSize the FIRST
// base's port equals the SECOND base's literal value — two listeners
// bound to the same port number, in the same dev stack.
//
// # The defect, concretely
//
// control-plane's deploy/kcl/dev/main.k declares:
//
//	_gateway_controller_port = fp.allocate_port(28090, _key)  // line 164
//	_gateway_grpc_port       = fp.allocate_port(29190, _key)  // line 155
//
// Both are ≡ 90 (mod 100 — the default BlockSize). They are 1100 apart,
// i.e. 11 blocks. At block 11, 28090 + 11*100 = 29190: the controller
// port for the 11th worktree equals the DEFAULT stack's gateway grpc
// port. Two unrelated listeners cross.
//
// This has shipped invisibly because config.DefaultMaxStacks is 8 — the
// reachable block range is 0..7, well short of 11. The moment MaxStacks
// is raised past 11 (the whole point of making it configurable — see
// [config.DevStackConfig]'s doc comment), this collision goes live. This
// rule is the prerequisite that makes raising the ceiling safe: it is
// meant to be run, and to newly fire, as part of evaluating any
// max_stacks increase.
//
// # Why the finding depends on max_stacks — load-bearing, not a bug
//
// Condition (b) below — is the colliding block actually reachable at the
// project's configured max_stacks — is what keeps this rule actionable
// rather than noise. Every base pair that merely shares a remainder mod
// BlockSize is a LATENT instance of the same defect class (see the
// 8090/28090 pair below, 200 blocks apart), but flagging all of them
// unconditionally would make the rule fire on every dev/main.k that ever
// existed, most of which will never actually run 200 parallel stacks. A
// reader who raises max_stacks and reruns `forge lint` and sees a NEW
// finding that did not exist yesterday is seeing the rule do exactly
// what it is for.
//
// # What is scanned
//
// The analyzer walks deploy/kcl/**/*.k for `allocate_port(<int>, ...)`
// call sites, matching on the function name only — it does not assume
// the `fp` alias real projects use for `import kcl_plugin.forge as fp`.
// KCL's `#` line comments are stripped (quote-aware, so a `#` inside a
// string literal is not mistaken for one) before matching, so a comment
// that merely MENTIONS `allocate_port(28090, ...)` for exposition (this
// package's own doc comment above does exactly that) is not double
// counted as a real call site.
//
// Pairs are compared WITHIN one file only. Two different deploy
// environments (dev/main.k vs prod/main.k) do not share a running
// process group or a k3d port-mapping table, so a base collision across
// them is not a reachable defect — scoping per file is what keeps
// `3000` appearing once per environment from being flagged against
// itself.
//
// # Severity: error, not warning
//
// Unlike forgeconv's heuristic analyzers (handler-file-size,
// no-handler-error-mapping), there is no false-positive risk here: the
// arithmetic is exact, not a pattern guess, and a fired finding
// describes a real port collision that WILL happen the moment a
// worktree's block reaches the stated number. That is the same bar
// forgeconv-unwrapped-domain-error and forgeconv-one-service-per-file
// hold their callers to — a structural certainty, not a style nit — so
// this rule gates the build like they do.
package forgeconv

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// reAllocatePortCall matches an allocate_port call's integer base
// literal. \b anchors on "allocate_port" itself (not on any alias/
// receiver before the dot), so `fp.allocate_port(`, a bare
// `allocate_port(`, or any other alias all match; a name that merely
// ENDS in "allocate_port" (e.g. `my_allocate_port(`) does not, because
// `_` is a word character and \b requires a word/non-word transition.
var reAllocatePortCall = regexp.MustCompile(`\ballocate_port\s*\(\s*(\d+)\s*,`)

// allocatePortCall is one allocate_port(base, ...) call site.
type allocatePortCall struct {
	Base int
	File string // relative to rootDir
	Line int    // 1-indexed
}

// LintAllocatePortSpacing walks rootDir/deploy/kcl for *.k files and
// flags pairs of allocate_port base literals, within the same file,
// that collide at a reachable block. maxStacks and blockSize should
// come from the project's config.DevStackConfig (EffectiveMaxStacks /
// EffectiveBlockSize) — a value <= 0 is treated as "unset" and falls
// back to the same config.Default* the allocator itself uses, so a
// caller that doesn't have a loaded project config yet still gets a
// meaningful scan.
//
// A missing deploy/kcl directory is not an error — CLI/library projects
// and any project that hasn't scaffolded a deploy tree yet have nothing
// to scan.
func LintAllocatePortSpacing(rootDir string, maxStacks, blockSize int) (Result, error) {
	if maxStacks <= 0 {
		maxStacks = config.DefaultMaxStacks
	}
	if blockSize <= 0 {
		blockSize = config.DefaultBlockSize
	}

	kclDir := filepath.Join(rootDir, "deploy", "kcl")
	if _, err := os.Stat(kclDir); os.IsNotExist(err) {
		return Result{}, nil
	}

	var files []string
	err := filepath.WalkDir(kclDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".k") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk %s: %w", kclDir, err)
	}
	sort.Strings(files)

	var result Result
	for _, f := range files {
		content, readErr := os.ReadFile(f) //nolint:gosec // lint walker drives paths
		if readErr != nil {
			return Result{}, fmt.Errorf("read %s: %w", f, readErr)
		}
		rel, relErr := filepath.Rel(rootDir, f)
		if relErr != nil {
			rel = f
		}
		calls := scanAllocatePortCalls(rel, string(content))
		result.Findings = append(result.Findings, findCollidingPairs(calls, maxStacks, blockSize)...)
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

// scanAllocatePortCalls extracts every allocate_port(<int literal>, ...)
// call site from one .k file's content, in ascending line order.
func scanAllocatePortCalls(relPath, content string) []allocatePortCall {
	var calls []allocatePortCall
	for i, line := range strings.Split(content, "\n") {
		stripped := stripKCLComment(line)
		matches := reAllocatePortCall.FindAllStringSubmatch(stripped, -1)
		if len(matches) == 0 {
			continue
		}
		for _, m := range matches {
			base, err := strconv.Atoi(m[1])
			if err != nil {
				continue // unreachable given \d+, but never let a parse panic a lint pass
			}
			calls = append(calls, allocatePortCall{Base: base, File: relPath, Line: i + 1})
		}
	}
	return calls
}

// stripKCLComment removes a KCL `#` line comment, honoring single- and
// double-quoted strings so a `#` inside a string literal isn't mistaken
// for a comment opener. It is a single-line heuristic — like
// stripGoComments in handler_file_size.go, it does not track KCL's
// triple-quoted long strings across lines. That is an acceptable gap for
// a lint heuristic scanning port literals: a base value hidden inside a
// multi-line string is not a real allocate_port call site either way.
func stripKCLComment(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && (inSingle || inDouble):
			i++ // skip the escaped character
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '#' && !inSingle && !inDouble:
			return line[:i]
		}
	}
	return line
}

// findCollidingPairs compares every pair of calls (within one file, since
// calls is always scoped to a single file's scan) and returns a finding
// for each pair whose bases are congruent mod blockSize AND whose
// separation in blocks is reachable at maxStacks. calls is assumed to be
// in ascending-line order (scanAllocatePortCalls's contract), which
// makes calls[i].Line <= calls[j].Line for i < j — used to pick a stable
// finding line without an extra sort.
func findCollidingPairs(calls []allocatePortCall, maxStacks, blockSize int) []Finding {
	var findings []Finding
	for i := 0; i < len(calls); i++ {
		for j := i + 1; j < len(calls); j++ {
			a, b := calls[i], calls[j]
			if a.Base == b.Base && a.Line == b.Line {
				continue // same literal call matched twice by an odd line shape; not two sites
			}

			small, large := a, b
			if large.Base < small.Base {
				small, large = large, small
			}
			remainder := small.Base % blockSize
			if large.Base%blockSize != remainder {
				continue // not congruent — e.g. 3000 vs 3091 mod 100 (0 vs 91): never a collision
			}

			deltaBlocks := (large.Base - small.Base) / blockSize
			if deltaBlocks >= maxStacks {
				continue // congruent but unreachable at this project's stack ceiling — not actionable
			}

			collidingPort := small.Base + deltaBlocks*blockSize // == large.Base, by construction

			findings = append(findings, Finding{
				Rule:     "forgeconv-allocate-port-spacing",
				Severity: SeverityError,
				File:     a.File,
				Line:     a.Line,
				Message: fmt.Sprintf(
					"allocate_port(%d) at %s:%d and allocate_port(%d) at %s:%d are congruent mod %d "+
						"(both ≡ %d): at block %d, %d + %d*%d = %d, colliding with the %d base — "+
						"and block %d is reachable because max_stacks=%d (raising max_stacks can newly "+
						"expose this collision even if it doesn't fire today)",
					small.Base, small.File, small.Line,
					large.Base, large.File, large.Line,
					blockSize, remainder,
					deltaBlocks, small.Base, deltaBlocks, blockSize, collidingPort,
					large.Base,
					deltaBlocks, maxStacks,
				),
				Remediation: fmt.Sprintf(
					"move one of the two bases to a value with a different remainder mod %d "+
						"(e.g. change %d or %d by an amount not divisible by %d) — "+
						"see the DevStackConfig doc comment in internal/config/config.go for the spacing invariant",
					blockSize, small.Base, large.Base, blockSize,
				),
			})
		}
	}
	return findings
}
