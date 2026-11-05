// Package forgeconv implements lint rules that enforce forge codegen
// conventions on proto files. The analyzers here catch shapes that would
// otherwise fail silently or blow up at generate time, surfacing them as
// actionable lint findings with explicit remediation before
// `forge generate` runs.
//
// The full list of rules:
//
//	forgeconv-one-service-per-file      one service per .proto, full stop
//	forgeconv-service-dir-consistency   proto service name must match its
//	                                    proto/services/<dir>/ directory
//	forgeconv-list-filter-optional      a List request's scalar/enum filter
//	                                    fields must be `optional`
//	                                    (see list_filter_optional.go)
//	forgeconv-activity-declared         every mutating rpc declares its
//	                                    activity event, or `none`
//	                                    (see activity_declared.go)
//	forgeconv-allocate-port-spacing     two allocate_port() base literals
//	                                    congruent mod BlockSize collide at
//	                                    a block reachable within MaxStacks
//	                                    (see allocate_port_spacing.go)
//	forgeconv-config-service-name       a config field must not bind
//	                                    OTEL_SERVICE_NAME, a per-workload
//	                                    fact forge.render derives
//	                                    (see config_service_name.go)
//	forgeconv-bun-dollar-placeholder    `$N` in SQL bun formats (NewRaw,
//	                                    the builders), which binds only `?`
//	forgeconv-bun-literal-question-mark a `?` bun will bind by mistake: in
//	                                    a comment, a string literal, or a
//	                                    jsonb ?/?|/?& operator
//	                                    (see bun_placeholders.go)
//	forgeconv-tx-lock-under-serializable a SERIALIZABLE transaction whose
//	                                    body takes a lock (FOR UPDATE, an
//	                                    advisory lock, LOCK TABLE); warning,
//	                                    error under --strict
//	                                    (see tx_lock_under_serializable.go)
//	forgeconv-deps-surface              a Deps interface field declares
//	                                    methods the package never calls
//	forgeconv-no-container-deps         a Deps field holds a container
//	                                    (internal/app types, cmd.Deps, a
//	                                    locator, `any`)
//	forgeconv-deps-no-downcast          a type assertion from a Deps field
//	                                    to a concrete type
//	forgeconv-no-getenv                 a component package reads the
//	                                    process environment itself
//	forgeconv-deps-root-config          a Deps field holds the root config
//	                                    (--strict only, a warning)
//	                                    (see least_knowledge.go)
//
// `auth_required` is lint-free here: it is enforced at runtime (projected
// into pkg/middleware/procedures_gen.go, read fail-closed by the auth
// interceptor), and forge reads no authorization annotations beyond it.
//
// The package exposes a single LintProtoTree entry point that takes a
// project root (or any directory containing .proto files) and returns a
// Result. Findings are emitted in deterministic order (file, then byte
// position) so output is stable across runs.
package forgeconv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// Severity and Finding now live in the shared internal/linter/finding
// package — these aliases keep the historical forgeconv.* spellings
// working for callers and tests while the underlying vocabulary is
// single-sourced. forgeconv findings populate
// Rule/Severity/File/Line/Message/Remediation.
type (
	// Severity is the shared finding severity vocabulary, re-exported
	// under the historical forgeconv spelling.
	Severity = finding.Severity
	// Finding is the shared linter finding shape, re-exported under the
	// historical forgeconv spelling. forgeconv findings populate
	// Rule/Severity/File/Line/Message/Remediation.
	Finding = finding.Finding
)

// Severity enum values (aliases onto the canonical single-spelling set).
const (
	SeverityError   = finding.SeverityError
	SeverityWarning = finding.SeverityWarning
)

// Result aggregates findings from a single lint run. It is a distinct
// type (not an alias) so forgeconv can hang its own FormatText rendering
// on it; the finding vocabulary inside is the shared one.
type Result struct {
	Findings []Finding `json:"findings"`
}

// HasErrors returns true if any finding has Severity == SeverityError.
// Used by `forge lint` to decide exit status.
func (r Result) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// FormatText renders findings as a human-readable report. Empty result
// produces an empty string so callers can prefix their own success line.
func (r Result) FormatText() string {
	if len(r.Findings) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d forge convention violation(s):\n\n", len(r.Findings))
	for _, f := range r.Findings {
		icon := "✗"
		if f.Severity == SeverityWarning {
			icon = "⚠"
		}
		if f.Line > 0 {
			fmt.Fprintf(&sb, "  %s [%s] %s:%d\n      %s\n", icon, f.Rule, f.File, f.Line, f.Message)
		} else {
			fmt.Fprintf(&sb, "  %s [%s] %s\n      %s\n", icon, f.Rule, f.File, f.Message)
		}
		if f.Remediation != "" {
			fmt.Fprintf(&sb, "      → %s\n", f.Remediation)
		}
	}
	return sb.String()
}

// LintOptions tunes the proto convention analyzers. The zero value is
// the default (advisory) posture; callers opt into stricter gating.
type LintOptions struct {
	// Strict escalates advisory findings to errors: the least-knowledge
	// rules and forgeconv-tx-lock-under-serializable. No proto rule
	// escalates on it.
	Strict bool
}

// LintProtoTree walks rootDir for .proto files and runs every analyzer
// in the default (advisory) posture. Thin wrapper over LintProtoTreeOpts
// kept for the existing call sites + tests.
func LintProtoTree(rootDir string) (Result, error) {
	return LintProtoTreeOpts(rootDir, LintOptions{})
}

// LintProtoTreeOpts walks rootDir for .proto files and runs every
// analyzer with the supplied options. Files under proto/forge/ (vendored
// forge annotation protos) are skipped — they're external definitions,
// not user code. Returns a deterministic Result ordered by
// (file, line, rule).
func LintProtoTreeOpts(rootDir string, opts LintOptions) (Result, error) {
	var protoFiles []string
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip vendored forge annotations, the gen output dir, and
			// anything resembling a buf cache. Keeps the linter focused
			// on user-authored proto.
			base := info.Name()
			if base == ".buf" || base == "node_modules" || base == "gen" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".proto") {
			return nil
		}
		// Skip vendored forge annotation file specifically — it's external.
		if strings.Contains(filepath.ToSlash(path), "/proto/forge/") {
			return nil
		}
		protoFiles = append(protoFiles, path)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk %s: %w", rootDir, err)
	}

	sort.Strings(protoFiles)

	// Tree-level rules need every file before they can judge any one of
	// them (an enum in proto/shared/ types a filter in proto/services/), so
	// they run once up front; their findings join each file's own below and
	// go through the same suppression pass.
	treeFindings := checkListFilterOptional(rootDir, protoFiles)
	if treeFindings == nil {
		treeFindings = map[string][]Finding{}
	}
	for rel, fs := range checkActivityDeclared(rootDir, protoFiles) {
		treeFindings[rel] = append(treeFindings[rel], fs...)
	}

	var result Result
	consumed := map[string]bool{}
	for _, file := range protoFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			return Result{}, fmt.Errorf("read %s: %w", file, err)
		}
		// Resolve to a path relative to rootDir for stable output across
		// machines (no /tmp/abc123 prefixes leaking into CI logs).
		rel, relErr := filepath.Rel(rootDir, file)
		if relErr != nil {
			rel = file
		}
		// The shared suppression engine, over the content already in
		// hand: `// forge:lint-disable-next-line <rule>: <why>` above the
		// offending declaration, and a reasonless one of a gating rule is
		// itself reported. Applied here rather than by the caller so
		// every consumer (lint, audit) sees the same verdict.
		consumed[rel] = true
		findings := append(lintProtoFile(rel, string(content), opts), treeFindings[rel]...)
		applied := suppress.Apply(string(content), findings)
		result.Findings = append(result.Findings, applied.Kept...)
		result.Findings = append(result.Findings, applied.Violations...)
	}

	// Tree findings about no walked file — the descriptor itself, keyed
	// "" — have no source to suppress against; they are reported as is.
	for rel, fs := range treeFindings {
		if !consumed[rel] {
			result.Findings = append(result.Findings, fs...)
		}
	}

	// Stable ordering: by file, then line, then rule.
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

// lintProtoFile runs every rule against the parsed view of one .proto
// file. Exposed for testability (the test suite renders fake proto
// content via this function rather than going through filesystem walk).
func lintProtoFile(relPath, content string, opts LintOptions) []Finding {
	var findings []Finding
	pf := parseProtoFile(relPath, content)

	findings = append(findings, checkOneServicePerFile(pf)...)
	findings = append(findings, checkServiceDirConsistency(pf)...)
	findings = append(findings, checkConfigServiceName(relPath, content)...)
	_ = opts // reserved: no rule currently escalates on LintOptions.Strict

	return findings
}

// ─── Rule 1: one service per .proto file ─────────────────────────────────────

func checkOneServicePerFile(pf parsedProto) []Finding {
	if len(pf.Services) <= 1 {
		return nil
	}
	// Report on the SECOND and subsequent services — the first one is
	// the canonical one to keep, and that's where we'd point the user
	// to split out additional services.
	var findings []Finding
	for _, svc := range pf.Services[1:] {
		findings = append(findings, Finding{
			Rule:     "forgeconv-one-service-per-file",
			Severity: SeverityError,
			File:     pf.Path,
			Line:     svc.Line,
			Message: fmt.Sprintf(
				"file declares %d services (%s); forge convention is one service per .proto file",
				len(pf.Services), serviceList(pf.Services)),
			Remediation: fmt.Sprintf(
				"split %q into its own .proto file at %s/%s.proto (see the proto-split skill)",
				svc.Name, filepath.Dir(pf.Path), strings.ToLower(svc.Name)),
		})
	}
	return findings
}

func serviceList(services []protoService) string {
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// ─── Rule 2: proto service name must match its directory ───────────────────
//
// Only checks files under proto/services/<dir>/ — the layout
// ServiceNameFromProtoFile recognizes. Flat/multi-service proto
// packages (proto/controlplane/v1/) have no per-service directory to
// compare against and are skipped. Delegates the actual identifier
// comparison to cmdutil.ValidateServiceDirConsistency so `forge lint
// --conventions` and `forge generate` can never disagree about which
// proto/directory pairs are compatible.
func checkServiceDirConsistency(pf parsedProto) []Finding {
	dir := codegen.ServiceNameFromProtoFile(filepath.ToSlash(pf.Path))
	if dir == "" {
		return nil
	}
	var findings []Finding
	for _, svc := range pf.Services {
		err := cmdutil.ValidateServiceDirConsistency(svc.Name, dir)
		if err == nil {
			continue
		}
		findings = append(findings, Finding{
			Rule:        "forgeconv-service-dir-consistency",
			Severity:    SeverityError,
			File:        pf.Path,
			Line:        svc.Line,
			Message:     err.Error(),
			Remediation: "rename the proto service or the proto/services/ directory so their logical names agree (ignoring separators and casing)",
		})
	}
	return findings
}

// ─── proto file mini-parser ──────────────────────────────────────────────────
//
// Full proto parsing requires a dependency on a proto AST library; for
// lint-purposes a regex-based line scan is plenty. The parser tracks:
//
//   - service blocks (name + line)
//   - message blocks (name + line)
//   - field declarations inside top-level message blocks
//
// It does NOT parse nested messages, oneofs, options, or imports — those
// don't affect any of the rules above.

type parsedProto struct {
	Path     string
	Services []protoService
	Messages []protoMessage
}

type protoService struct {
	Name string
	Line int
	// Methods lists the RPC declarations inside this service block.
	Methods []protoMethod
}

// protoMethod is one `rpc Name(Req) returns (Resp)` declaration, captured
// during the service-block scan the one-service-per-file rule drives.
type protoMethod struct {
	Name string
	Line int
	// HasMethodAnnotation is true when the RPC body declares
	// `option (forge.v1.method) = { ... }`. Parsed but not currently
	// consumed by any rule.
	HasMethodAnnotation bool
}

type protoMessage struct {
	Name   string
	Line   int
	Fields []protoField
}

type protoField struct {
	Name   string
	Type   string
	Number int
	Line   int
}

var (
	// reService matches `service Foo {`. Not anchored to ^ so a single
	// line carrying two service decls (which is itself a violation that
	// rule #1 catches) reports both names.
	reService = regexp.MustCompile(`(?:^|\W)service\s+(\w+)\s*\{`)
	reMessage = regexp.MustCompile(`^\s*message\s+(\w+)\s*\{`)
	// Field declaration: optional `optional`/`repeated` qualifier, type,
	// name, `=`, number. Trailing `[ ... ]` annotation block (if any) is
	// captured separately by the line-aggregation logic below.
	reField = regexp.MustCompile(`^\s*(?:(?:optional|repeated)\s+)?([\w.]+)\s+(\w+)\s*=\s*(\d+)`)
	// reRPC matches `rpc MethodName(` at the start of an RPC declaration.
	// The remainder (request/response/options block) is handled by the
	// brace/annotation scanner so multi-line RPC bodies are supported.
	reRPC       = regexp.MustCompile(`^\s*rpc\s+(\w+)\s*\(`)
	reMethodOpt = regexp.MustCompile(`\(forge\.v1\.method\)`)
)

// parseProtoFile is a forgiving line-and-brace-counting scanner. It
// produces a parsedProto struct with everything the lint rules need,
// without taking on a real proto AST dependency. The parser must
// handle:
//
//   - multi-line option blocks (`option (forge.v1.entity) = { ... };`)
//   - field annotations that span multiple lines:
//     string id = 1 [(forge.v1.field) = {
//     pk: true
//     }];
//   - nested braces (entity options blocks contain `indexes: [{...}]`)
//
// We track `messageDepth`, `optionDepth` (depth INSIDE a message option
// block — i.e. inside `option (forge.v1.entity) = { ... };`), and
// `fieldAnnoDepth` (inside a field's `[ ... ]` annotation). A field's
// annotation block can contain nested braces (`{ pk: true }`), so we
// follow `[` / `]` to know when we leave it.
// protoScanState holds the mutable state threaded through the line-based
// proto scanner. The scanning logic used to live in one large function;
// it is now split across focused per-construct helpers that operate on
// this state. Behavior (parse results, ordering of emitted entities, and
// every edge case) is identical to the original single-function form.
type protoScanState struct {
	pf parsedProto

	lineNum    int
	braceDepth int

	// Message-block tracking.
	inMessage         bool
	currentMessage    *protoMessage
	messageBraceDepth int

	// Service-block tracking: when inside a `service Foo { ... }` block
	// we scan for `rpc` declarations (the one-service-per-file rule needs
	// the service inventory).
	inService         bool
	currentService    *protoService
	serviceBraceDepth int
	// pendingRPC holds an RPC-in-progress whose options block may span
	// multiple lines (`rpc X(..) returns (..) { option (...) = {..}; }`).
	// We keep accumulating annotation presence until the RPC body
	// closes (or, for single-line `rpc X(..) returns (..);`, immediately).
	pendingRPC   *protoMethod
	rpcBodyDepth int

	// pendingField holds a field-in-progress when the field's annotation
	// (the [...] part) spans multiple lines.
	pendingField        *protoField
	pendingBracketDepth int
}

func parseProtoFile(path, content string) parsedProto {
	s := &protoScanState{pf: parsedProto{Path: path}}

	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		s.lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || trimmed == "" {
			// Still count the braces inside multi-line options blocks
			// in case someone puts braces inside a comment — but
			// realistically not worth it. Skip the line entirely.
			continue
		}
		// Comment-stripping: drop everything after `//` so we don't
		// false-positive on annotation strings inside comments.
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
			trimmed = strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
		}

		if s.maybeStartService(line, trimmed) {
			continue
		}
		if s.inService {
			s.scanServiceLine(line, trimmed)
			continue
		}
		if s.handleOutsideMessage(line, trimmed) {
			continue
		}
		s.scanMessageBodyLine(line, trimmed)
	}

	return s.pf
}

// maybeStartService handles a top-level `service Foo { ... }` opener and
// reports whether the line was fully consumed (caller should `continue`).
//
// Multiple service decls on one line (`service Foo {} service Bar {}`)
// are rare in practice but a common bad-fixture shape, and the smoke
// test in `forge lint`'s docs explicitly does this. Use FindAll so each
// service is recorded.
func (s *protoScanState) maybeStartService(line, trimmed string) bool {
	if s.inMessage || s.inService || s.braceDepth != 0 {
		return false
	}
	matches := reService.FindAllStringSubmatch(trimmed, -1)
	if len(matches) == 0 {
		return false
	}
	for _, m := range matches {
		s.pf.Services = append(s.pf.Services, protoService{Name: m[1], Line: s.lineNum})
	}
	netBraces := strings.Count(line, "{") - strings.Count(line, "}")
	// A single-line `service Foo {}` opens and closes on the same line —
	// don't enter the block. Only the canonical multi-line form (net
	// brace depth > 0) becomes the active service we scan RPCs inside;
	// multi-service-per-line is a rule-1 violation anyway, so scan only
	// the first.
	if netBraces > 0 && len(matches) == 1 {
		s.inService = true
		s.currentService = &s.pf.Services[len(s.pf.Services)-1]
		s.serviceBraceDepth = netBraces
	} else {
		s.braceDepth += netBraces
	}
	return true
}

// scanServiceLine scans one line inside an open service block, tracking
// RPC declarations and per-RPC (forge.v1.method) annotations. RPC bodies
// may be single-line (`rpc X(..) returns (..);`) or span multiple lines
// with an `{ option (forge.v1.method) = {..}; }` body.
func (s *protoScanState) scanServiceLine(line, trimmed string) {
	netBraces := strings.Count(line, "{") - strings.Count(line, "}")
	s.serviceBraceDepth += netBraces
	if s.pendingRPC != nil {
		s.advancePendingRPC(line, netBraces)
	} else if m := reRPC.FindStringSubmatch(trimmed); m != nil {
		s.startRPC(line, m, netBraces)
	}
	// Leaving the service block.
	if s.serviceBraceDepth <= 0 {
		if s.pendingRPC != nil {
			s.currentService.Methods = append(s.currentService.Methods, *s.pendingRPC)
			s.pendingRPC = nil
		}
		s.inService = false
		s.currentService = nil
		s.serviceBraceDepth = 0
		s.rpcBodyDepth = 0
	}
}

// advancePendingRPC continues accumulating an open RPC body's annotation
// presence and flushes the RPC once its body closes.
func (s *protoScanState) advancePendingRPC(line string, netBraces int) {
	if reMethodOpt.MatchString(line) {
		s.pendingRPC.HasMethodAnnotation = true
	}
	s.rpcBodyDepth += netBraces
	if s.rpcBodyDepth <= 0 {
		s.currentService.Methods = append(s.currentService.Methods, *s.pendingRPC)
		s.pendingRPC = nil
		s.rpcBodyDepth = 0
	}
}

// startRPC records a newly-detected RPC declaration, either flushing it
// immediately (single-line form) or holding it open (multi-line body).
func (s *protoScanState) startRPC(line string, m []string, netBraces int) {
	method := protoMethod{Name: m[1], Line: s.lineNum}
	if reMethodOpt.MatchString(line) {
		method.HasMethodAnnotation = true
	}
	// Count only braces that open the RPC body `{ ... }`, not the
	// `( ... )` request/response parens. If the line has a net-positive
	// brace depth the body is open across lines.
	if netBraces > 0 {
		s.pendingRPC = &method
		s.rpcBodyDepth = netBraces
	} else {
		s.currentService.Methods = append(s.currentService.Methods, method)
	}
}

// handleOutsideMessage handles lines seen while not inside a message
// block: it opens a new top-level message or tracks brace depth for
// non-message decls (e.g. enum blocks) so we don't pick up their fields.
// It reports whether the line was consumed (caller should `continue`);
// it returns false only when we are inside a message and the line needs
// field-level scanning.
func (s *protoScanState) handleOutsideMessage(line, trimmed string) bool {
	// Detect message blocks (top-level only — nested messages don't
	// participate in entity annotations in practice).
	if !s.inMessage && s.braceDepth == 0 {
		if m := reMessage.FindStringSubmatch(trimmed); m != nil {
			s.pf.Messages = append(s.pf.Messages, protoMessage{Name: m[1], Line: s.lineNum})
			s.currentMessage = &s.pf.Messages[len(s.pf.Messages)-1]
			s.inMessage = true
			s.messageBraceDepth = 1
			return true
		}
		s.braceDepth += strings.Count(line, "{")
		s.braceDepth -= strings.Count(line, "}")
		return true
	}
	if !s.inMessage {
		s.braceDepth += strings.Count(line, "{")
		s.braceDepth -= strings.Count(line, "}")
		return true
	}
	return false
}

// scanMessageBodyLine scans one line inside an open message block: it
// updates brace depth, accumulates multi-line field annotations, and
// detects new field declarations, closing the message when its brace
// depth returns to zero.
func (s *protoScanState) scanMessageBodyLine(line, trimmed string) {
	// We're inside a message block. Update brace depth.
	opens := strings.Count(line, "{")
	closes := strings.Count(line, "}")
	s.messageBraceDepth += opens - closes

	// Continue accumulating multi-line field annotations.
	if s.pendingField != nil {
		s.advancePendingField(line)
		return
	}

	s.maybeDetectField(line, trimmed)

	if s.messageBraceDepth <= 0 {
		s.inMessage = false
		s.currentMessage = nil
		s.messageBraceDepth = 0
	}
}

// advancePendingField accumulates a field's multi-line `[ ... ]`
// annotation and flushes the field once the annotation closes. The line
// is consumed as part of the field annotation, so the field's `];` does
// not itself close the message — but we still honor a message that
// closed on the same line.
func (s *protoScanState) advancePendingField(line string) {
	s.pendingBracketDepth += strings.Count(line, "[") - strings.Count(line, "]")
	if s.pendingBracketDepth <= 0 {
		// Done — flush.
		s.currentMessage.Fields = append(s.currentMessage.Fields, *s.pendingField)
		s.pendingField = nil
		s.pendingBracketDepth = 0
	}
	if s.messageBraceDepth <= 0 {
		s.inMessage = false
		s.currentMessage = nil
	}
}

// maybeDetectField tries to detect a new field declaration on this line,
// either flushing it immediately or holding it open when its annotation
// spans multiple lines (opens `[` without closing `]`).
func (s *protoScanState) maybeDetectField(line, trimmed string) {
	if s.messageBraceDepth < 1 {
		return
	}
	m := reField.FindStringSubmatch(trimmed)
	if m == nil || isProtoOptionLine(trimmed) {
		return
	}
	field := parseProtoFieldLine(m, s.lineNum)

	// Multi-line annotation: opens `[` without closing `]`.
	openBrackets := strings.Count(line, "[")
	closeBrackets := strings.Count(line, "]")
	if openBrackets > closeBrackets {
		s.pendingField = &field
		s.pendingBracketDepth = openBrackets - closeBrackets
		return
	}
	s.currentMessage.Fields = append(s.currentMessage.Fields, field)
}

// isProtoOptionLine reports whether a trimmed proto line is an option
// or reserved declaration dressed up to look like a field.
func isProtoOptionLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "option ") || strings.HasPrefix(trimmed, "reserved ")
}

// parseProtoFieldLine builds a protoField from a regex match.
func parseProtoFieldLine(m []string, lineNum int) protoField {
	num := 0
	_, _ = fmt.Sscanf(m[3], "%d", &num)
	return protoField{
		Name:   m[2],
		Type:   m[1],
		Number: num,
		Line:   lineNum,
	}
}
