package prpolicy

import (
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// RiskTier is how much review a PR needs: low needs no model, medium one
// pass, high the most careful review the repository runs. The values are
// the ones `pr_head_verdicts.risk_tier` and a review node's `risk_tier`
// carry.
type RiskTier string

const (
	RiskLow    RiskTier = "low"
	RiskMedium RiskTier = "medium"
	RiskHigh   RiskTier = "high"
)

// ReviewRule is why a verdict is what it is: `pr_head_verdicts.rule`.
type ReviewRule string

const (
	// RuleModel: a model reviewed the head.
	RuleModel ReviewRule = "model"
	// RuleNoModelLowRisk: the head is low risk and passed with no model.
	RuleNoModelLowRisk ReviewRule = "no_model_low_risk"
	// RuleGeneratedOnly: every changed file is generated, and the gate's
	// regen check proves its bytes.
	RuleGeneratedOnly ReviewRule = "generated_only"
	// RuleExactRevert: the head reverts a landed commit exactly. The caller
	// decides it from git, never from the diff's shape, so [AssessRisk]
	// never returns it.
	RuleExactRevert ReviewRule = "exact_revert"
)

// FileStatus is a changed file's status, as GitHub's pull request files API
// reports it.
type FileStatus string

const (
	FileAdded    FileStatus = "added"
	FileModified FileStatus = "modified"
	FileRemoved  FileStatus = "removed"
	FileRenamed  FileStatus = "renamed"
	FileCopied   FileStatus = "copied"
	FileChanged  FileStatus = "changed"
)

// ChangedFile is one file a PR changes.
type ChangedFile struct {
	// Path is repository-relative, with forward slashes.
	Path string
	// PreviousPath is the path before a rename or copy, else empty. A rename
	// is judged by both paths: moving a migration away is a migration change.
	PreviousPath string
	Status       FileStatus
	Additions    int
	Deletions    int
	// Generated is the caller's own knowledge that the file is generated (a
	// `Code generated … DO NOT EDIT.` header, say). A path matching the
	// repository's pipeline.yaml `generated` globs is generated whatever this
	// says.
	Generated bool
}

// Diff is a PR's changed files and its totals.
type Diff struct {
	Files []ChangedFile
	// Additions and Deletions are the PR's totals.
	Additions int
	Deletions int
	// ChangedFiles is how many files the PR changes, which can be more than
	// len(Files) when the list the caller read was cut short (GitHub lists
	// at most 3,000). Zero means Files is the whole list.
	ChangedFiles int
}

// NewDiff returns the Diff of files, with its totals summed from them.
func NewDiff(files ...ChangedFile) Diff {
	d := Diff{Files: files, ChangedFiles: len(files)}
	for _, f := range files {
		d.Additions += f.Additions
		d.Deletions += f.Deletions
	}
	return d
}

// The size limits of the medium tier. A change of code (documentation and
// generated files aside) past either is high: it is more than one careful
// pass reads. They are package constants for now and become pipeline.yaml
// knobs later.
const (
	HighRiskLines = 800
	HighRiskFiles = 40
)

// RiskAssessment is [AssessRisk]'s answer.
type RiskAssessment struct {
	Tier RiskTier
	// Rule is RuleNoModelLowRisk or RuleGeneratedOnly for a low tier, else
	// RuleModel.
	Rule ReviewRule
	// Reasons say, in plain words, why the change is in its tier. There is
	// at least one.
	Reasons []string
}

// NeedsModel reports whether a model must review the head.
func (a RiskAssessment) NeedsModel() bool { return a.Rule == RuleModel }

// ClassifyRisk is [AssessRisk]'s tier and reasons.
func ClassifyRisk(d Diff, cfg config.Resolved) (RiskTier, []string) {
	a := AssessRisk(d, cfg)
	return a.Tier, a.Reasons
}

// AssessRisk decides a PR's risk tier from its changed files: their paths,
// kinds and size, and nothing else. It is deterministic: the same diff and
// configuration always give the same answer, reasons included.
//
//   - High: the file list is incomplete, a file is on a high-risk path
//     (a migration, protobuf, auth or credential code, CI configuration,
//     `.reliant/**`, go.mod), or the code changed is past [HighRiskLines]
//     or [HighRiskFiles].
//   - Low, needing no model: every file is documentation or generated.
//     Generated means matching pipeline.yaml `generated` (or marked so by
//     the caller), and counts only when the repository declares `regen`:
//     the gate's regeneration check is then what proves the bytes. Without
//     `regen` nothing does, and a generated file is read like code.
//   - Medium: any other change of code.
//
// A documentation file is judged only by the `.reliant/**` rule, because
// prompts and skills there steer agents: a doc about auth is not auth code.
// A generated file the gate proves is judged by none.
func AssessRisk(d Diff, cfg config.Resolved) RiskAssessment {
	proven := cfg.Regen != ""
	var (
		high        = map[string][]string{} // high-path rule kind → paths
		docs, gen   []string
		code        []string
		unprovenGen []string
		codeLines   int
	)
	for _, f := range d.Files {
		paths := filePaths(f)
		isGen := f.Generated || matchesAny(cfg.Generated, paths)
		isDoc := allDocs(paths)
		switch {
		case isGen && proven:
			gen = append(gen, f.Path)
			continue
		case isDoc:
			if reliantRule.matchAny(paths) {
				high[reliantRule.kind] = append(high[reliantRule.kind], f.Path)
				continue
			}
			docs = append(docs, f.Path)
			continue
		}
		for _, r := range highPathRules {
			if r.matchAny(paths) {
				high[r.kind] = append(high[r.kind], f.Path)
			}
		}
		if isGen {
			unprovenGen = append(unprovenGen, f.Path)
		}
		code = append(code, f.Path)
		codeLines += f.Additions + f.Deletions
	}

	var reasons []string
	if total := d.ChangedFiles; total > len(d.Files) {
		reasons = append(reasons, fmt.Sprintf(
			"the PR changes %d files but only %d were listed, so the rest could be anywhere", total, len(d.Files)))
	}
	for _, r := range highPathRules {
		if ps := high[r.kind]; len(ps) > 0 {
			reasons = append(reasons, fmt.Sprintf("touches %s (%s): %s", r.what, listPaths(ps), r.why))
		}
	}
	if codeLines > HighRiskLines {
		reasons = append(reasons, fmt.Sprintf(
			"the change is large: %d changed lines of code (more than %d), more than one careful pass reads", codeLines, HighRiskLines))
	}
	if len(code) > HighRiskFiles {
		reasons = append(reasons, fmt.Sprintf(
			"the change is wide: %d files of code (more than %d), more than one careful pass reads", len(code), HighRiskFiles))
	}
	if len(reasons) > 0 {
		return RiskAssessment{Tier: RiskHigh, Rule: RuleModel, Reasons: reasons}
	}

	if len(code) > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%d changed lines of code in %d %s (documentation and generated files aside), none on a high-risk path",
			codeLines, len(code), plural(len(code), "file", "files")))
		if len(unprovenGen) > 0 {
			reasons = append(reasons, fmt.Sprintf(
				"%s %s marked generated, but the repository declares no `regen` command, so nothing proves %s bytes and a model reads %s",
				listPaths(unprovenGen), plural(len(unprovenGen), "is", "are"),
				plural(len(unprovenGen), "its", "their"), plural(len(unprovenGen), "it", "them")))
		}
		return RiskAssessment{Tier: RiskMedium, Rule: RuleModel, Reasons: reasons}
	}

	rule := RuleNoModelLowRisk
	switch {
	case len(docs) == 0 && len(gen) == 0:
		reasons = append(reasons, "the PR changes no files")
	case len(docs) == 0:
		rule = RuleGeneratedOnly
	}
	if len(docs) > 0 {
		reasons = append(reasons, fmt.Sprintf("%s %s documentation, which no build or test runs",
			listPaths(docs), plural(len(docs), "is", "are")))
	}
	if len(gen) > 0 {
		reasons = append(reasons, fmt.Sprintf("%s %s generated, and the gate's regeneration check (`%s`) proves %s bytes",
			listPaths(gen), plural(len(gen), "is", "are"), cfg.Regen, plural(len(gen), "its", "their")))
	}
	reasons = append(reasons, "a low-risk change needs no model review")
	return RiskAssessment{Tier: RiskLow, Rule: rule, Reasons: reasons}
}

// highPathRule is one kind of high-risk path.
type highPathRule struct {
	kind  string
	what  string // the reason reads "touches <what> (<paths>): <why>"
	why   string
	match func(p string) bool
}

func (r highPathRule) matchAny(paths []string) bool {
	for _, p := range paths {
		if r.match(p) {
			return true
		}
	}
	return false
}

var reliantRule = highPathRule{
	kind: "reliant",
	what: "Reliant configuration (`.reliant/`)",
	why:  "`.reliant/` holds the workflows, prompts and pipeline settings that decide what agents and the pipeline itself do",
	match: func(p string) bool {
		return hasDirSegment(p, ".reliant")
	},
}

// highPathRules are the high-risk path kinds, in the order their reasons
// are listed (whatever order the files came in).
var highPathRules = []highPathRule{
	{
		kind: "migration",
		what: "a database migration",
		why:  "a schema change outlives the code that made it, and the release before it must still run against it",
		match: func(p string) bool {
			return hasDirSegment(p, "migrations")
		},
	},
	{
		kind: "proto",
		what: "an API contract (protobuf)",
		why:  "older clients and daemons keep calling the API it defines, so a mistake here breaks them",
		match: func(p string) bool {
			base := path.Base(p)
			return path.Ext(base) == ".proto" ||
				base == "buf.yaml" || base == "buf.gen.yaml" || base == "buf.work.yaml" || base == "buf.lock"
		},
	},
	{
		kind:  "auth",
		what:  "auth or credential code",
		why:   "it decides who may do what and guards secrets, and a mistake here is a security hole",
		match: isAuthPath,
	},
	{
		kind: "ci",
		what: "CI configuration",
		why:  "it decides what the checks run, so a change to it can weaken every later check",
		match: func(p string) bool {
			switch {
			case strings.HasPrefix(p, ".github/"), strings.HasPrefix(p, ".gitlab/"),
				strings.HasPrefix(p, ".circleci/"), strings.HasPrefix(p, ".buildkite/"):
				return true
			}
			switch p {
			case ".gitlab-ci.yml", "Jenkinsfile", "Taskfile.yml", "Taskfile.yaml", ".pre-commit-config.yaml":
				return true
			}
			return false
		},
	},
	reliantRule,
	{
		kind: "gomod",
		what: "a Go module definition",
		why:  "it changes which dependency code is built in, which no line of this diff shows",
		match: func(p string) bool {
			base := path.Base(p)
			return base == "go.mod" || base == "go.work"
		},
	},
}

// authWords are the words that make a path auth or credential code, matched
// against whole words of each path segment (split at punctuation and
// camelCase) and pairs of adjacent words, so `api_key.go` is and `author.go`
// and `tokenizer.go` are not. A heuristic: it leans towards high.
var authWords = map[string]bool{
	"auth": true, "authn": true, "authz": true, "oauth": true, "oauth2": true, "oidc": true,
	"saml": true, "sso": true, "login": true, "credential": true, "credentials": true, "creds": true,
	"secret": true, "secrets": true, "token": true, "tokens": true, "apikey": true, "apikeys": true,
	"keyring": true, "permission": true, "permissions": true, "rbac": true, "acl": true, "acls": true,
	"crypto": true,
}

func isAuthPath(p string) bool {
	segs := strings.Split(p, "/")
	last := len(segs) - 1
	segs[last] = strings.TrimSuffix(segs[last], path.Ext(segs[last]))
	for _, s := range segs {
		words := splitWords(s)
		for i, w := range words {
			// A word, or two adjacent words read as one ("api_key", "apiKey").
			if authWords[w] || (i > 0 && authWords[words[i-1]+w]) {
				return true
			}
		}
	}
	return false
}

// splitWords splits s into lower-case words at every character that is not
// a letter or digit, and at each lower-to-upper camelCase boundary.
func splitWords(s string) []string {
	var (
		words []string
		cur   []rune
	)
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range s {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	return words
}

// docExts are documentation file extensions. Plain .txt is not one:
// requirements.txt and CMakeLists.txt are build inputs.
var docExts = map[string]bool{".md": true, ".mdx": true, ".markdown": true, ".rst": true, ".adoc": true}

// docNames are documentation files known by name, with or without an
// extension.
var docNames = map[string]bool{"LICENSE": true, "NOTICE": true, "AUTHORS": true, "COPYING": true}

// agentInstructionFiles are Markdown files an agent loads as instructions:
// they steer what agents do, so they are read like code, not as docs.
var agentInstructionFiles = map[string]bool{
	"CLAUDE.md": true, "AGENTS.md": true, "GEMINI.md": true, "reliant.md": true,
}

func isDoc(p string) bool {
	base := path.Base(p)
	if agentInstructionFiles[base] {
		return false
	}
	ext := path.Ext(base)
	return docExts[strings.ToLower(ext)] || docNames[strings.TrimSuffix(base, ext)] || docNames[base]
}

func allDocs(paths []string) bool {
	for _, p := range paths {
		if !isDoc(p) {
			return false
		}
	}
	return true
}

func matchesAny(globs []string, paths []string) bool {
	for _, g := range globs {
		for _, p := range paths {
			if ok, err := doublestar.Match(g, p); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// filePaths is a file's normalized path, and its previous path when it was
// renamed or copied.
func filePaths(f ChangedFile) []string {
	out := []string{normPath(f.Path)}
	if f.PreviousPath != "" && f.PreviousPath != f.Path {
		out = append(out, normPath(f.PreviousPath))
	}
	return out
}

// normPath makes p repository-relative with forward slashes: no leading
// "./" or "/", no "a//b" or "a/./b".
func normPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/")
}

// hasDirSegment reports whether one of p's directories is named dir.
func hasDirSegment(p, dir string) bool {
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] {
		if s == dir {
			return true
		}
	}
	return false
}

// listPaths names up to three paths and counts the rest.
func listPaths(paths []string) string {
	const shown = 3
	if len(paths) <= shown {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:shown], ", "), len(paths)-shown)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
