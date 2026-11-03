// File: internal/codegen/proto_rawscan.go
//
// Lightweight RAW proto scanning — the deliberately small, regex-grade
// reader over the .proto files a service owns (proto/services/<svc>/...),
// in the same spirit as the lightweight migration parsing in
// internal/database (ParseMigrationsForSchema): no protoc, no buf, no
// descriptor compilation. It exists for the affordances that must read
// the wire truth BEFORE the compiled descriptor knows it:
//
//   - the `// forge:entity` birth marker — the author's explicit
//     tablizing decision, written in the proto file they are already
//     editing. A marked message may be brand new (not yet reachable from
//     any RPC), so gen/forge_descriptor/ cannot carry it; the raw
//     file is the only truth that has it.
//   - staleness detection (`forge scaffold` / `scaffold rpc`
//     self-heal): an RPC declared in the raw proto but absent from the
//     descriptor means "run generate", mechanically.
//
// The scanner is line-oriented with brace-depth tracking; each line is
// additionally split into statement-grade segments (splitProtoStatements)
// so single-line bodies — `message Widget { string id = 1; }` — parse
// identically to their multi-line spelling. It handles the conventional
// proto layout forge itself scaffolds (and the shapes LLMs author into
// it). It is NOT a general protobuf parser — genuinely exotic layouts
// (group syntax) degrade to "field not captured", never to a wrong
// capture, and every consumer of this data treats absence as "skip with
// a note", matching the robustness-first posture of the affordances
// built on it.
//
// Marker grammar: a full-line comment `// forge:entity` (spacing
// variants tolerated: `//forge:entity`, `//   forge:entity`, trailing
// prose after the token allowed) that precedes a TOP-LEVEL `message X {`
// declaration, with only comment/blank lines between marker and message.
// The marker is consumed at birth only — once the entity's table exists
// in the applied schema it is inert; no lint, no alignment checking,
// ever (docs/design/VERTICAL_SCAFFOLDING.md §6).

package codegen

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// entityMarkerRE matches the `// forge:entity` marker line. `//\s*`
// tolerates the glued `//forge:entity` spelling; the trailing group
// tolerates prose after the token (`// forge:entity — orders table`).
//
// The marker NAME comes from the KnownProtoMarkers registry (proto_markers.go)
// rather than being spelled inline, so the vocabulary this scanner recognizes
// and the vocabulary `forge lint --proto-markers` enforces cannot drift.
var entityMarkerRE = protoMarkerFullLineRE(ProtoMarkerEntity)

// appendOnlyMarkerRE / softDeleteMarkerRE match the two entity-behavior
// markers, parsed exactly like entityMarkerRE (leading full-line comment,
// spacing/prose variants tolerated). Each ALSO implies entity-ness — a
// message you mark append-only or soft-delete is one you want tabled — so a
// message carrying either is birthed without needing a separate
// `// forge:entity` line (see the pendingMarker handling in ScanRawProtoDir).
//
//   - `// forge:append-only` → born WITHOUT Update/Delete RPCs and with a DB
//     guard rejecting UPDATE/DELETE (an audit/ledger table).
//   - `// forge:soft-delete`  → born with a nullable deleted_at column
//     (Delete becomes soft, reads filter IS NULL). OPT-IN: unmarked
//     entities get no deleted_at.
var (
	appendOnlyMarkerRE = protoMarkerFullLineRE(ProtoMarkerAppendOnly)
	softDeleteMarkerRE = protoMarkerFullLineRE(ProtoMarkerSoftDelete)
)

// unmanagedMarkerRE matches the SERVICE-level `// forge:unmanaged` marker,
// with the entity markers' placement rule: a full-line comment preceding
// `service X {` with only comment/blank lines between.
var unmanagedMarkerRE = protoMarkerFullLineRE(ProtoMarkerUnmanaged)

// readOnlyFieldMarkerRE matches the FIELD-level read-only markers
// (ReadOnlyProtoMarkers: `// forge:read-only`, `// forge:computed` and
// `// forge:generated`, whose expression GeneratedMarkerExpr reads), in
// two spellings the scanner accepts: a full-line comment PRECEDING the
// field (the documented site, mirroring the entity markers) or a TRAILING
// inline comment (`string status = 4; // forge:read-only`). It is anchored
// with `//+` but NOT to line start, so the same expression matches the
// trailing form off the original field line. Either marker keeps the
// column + entity/read surface but excludes the field from the born
// Create/Update request messages; see SchemaFieldDef.ReadOnly.
//
// forge:computed rides the SAME expression rather than a parallel one so
// the two can never diverge on which fields stay client-writable; its
// extra obligation (something must populate the value) is checked by lint,
// which is the only place the two markers differ.
var readOnlyFieldMarkerRE = ProtoMarkerAnyLineRE(ReadOnlyProtoMarkers)

// RawProtoMessage is one TOP-LEVEL message captured by the raw scan.
type RawProtoMessage struct {
	// Name is the message's relative name within the package ("Order").
	Name string
	// File is the absolute path of the declaring .proto file.
	File string
	// Package is the file's proto package.
	Package string
	// Marked reports whether ANY birth marker (`// forge:entity`,
	// `// forge:append-only`, `// forge:soft-delete`) precedes the
	// declaration — all three tablize the message.
	Marked bool
	// AppendOnly reports a `// forge:append-only` marker: the entity is
	// born without Update/Delete RPCs and with a DB guard rejecting
	// UPDATE/DELETE.
	AppendOnly bool
	// SoftDeleteMarked reports a `// forge:soft-delete` marker: the birth
	// migration adds a nullable deleted_at column (opt-in soft delete).
	SoftDeleteMarked bool
	// Fields are the message's top-level fields in declaration order,
	// classified into the same SchemaFieldDef shape the compiled
	// descriptor uses (fully-qualified TypeNames, oneof membership, map
	// kinds) so downstream renderers consume one vocabulary regardless
	// of which truth supplied the fields.
	Fields []SchemaFieldDef
	// BodyOpen / BodyClose are byte offsets into the declaring file: the
	// position just AFTER the message's opening `{`, and the position OF
	// its matching `}`. They exist so a birth-time editor can append
	// fields to the message the author wrote without reformatting it
	// (stripBlockComments preserves length, so these index the raw file
	// bytes too).
	BodyOpen  int
	BodyClose int
	// MaxFieldNumber is the highest field number declared directly in this
	// message — including `reserved` numbers, and the fields of any oneof
	// declared in it (one number space), but NOT nested messages/enums
	// (their own space). An appended field takes MaxFieldNumber+1 and can
	// therefore never collide with, or renumber, what the author wrote.
	MaxFieldNumber int
	// UnappliedReadOnlyMarkers are read-only-family field markers
	// (ReadOnlyProtoMarkers: `// forge:read-only`, `// forge:computed`,
	// `// forge:generated`) written inside this message that attached to NO
	// captured field. A marker the scanner cannot honour must never be
	// dropped in silence — the author believes the field is off the write
	// surface and it would ship on the born Create request — so birth
	// refuses the entity and names the site.
	UnappliedReadOnlyMarkers []RawProtoMarkerSite
	// GeneratedMarkers are the `// forge:generated <expr>` markers bound to
	// a field of this message, one per marker, in declaration order. The
	// expression also lands on the field (SchemaFieldDef.Generated); this
	// ledger adds the SITE, so a birth that refuses the expression — empty,
	// on a field with no single column, or rejected by postgres — names the
	// file and line the author has to fix.
	GeneratedMarkers []GeneratedFieldMarker
	// RetiredMarkers are `forge:*` spellings written inside this message
	// that forge USED to recognize and deliberately removed
	// (RemovedProtoMarkers) — today they read as ordinary prose and do
	// nothing at all.
	//
	// This is the ledger UnappliedReadOnlyMarkers structurally cannot
	// populate: that one holds markers forge RECOGNIZES but could not
	// place, and a retired spelling is recognized by nothing. Without this
	// field the most dangerous case — `// forge:server-set`, whose author
	// believes a field is off the write surface — exits zero in silence.
	RetiredMarkers []RetiredProtoMarkerSite
}

// RetiredProtoMarkerSite locates one retired `forge:*` spelling and carries
// the marker that replaced it, so the refusal can name the fix rather than
// only the problem.
type RetiredProtoMarkerSite struct {
	// File is the absolute path of the declaring .proto file.
	File string
	// Line is the 1-based line number of the marker.
	Line int
	// Text is the marker's source line, trimmed.
	Text string
	// Marker is the retired spelling found (e.g. forge:server-set).
	Marker string
	// ReplacedBy is the marker that superseded it (e.g. forge:read-only).
	ReplacedBy string
}

// String renders the site as `<file>:<line>: <source line>`, matching
// RawProtoMarkerSite so both ledgers read alike in a refusal.
func (s RetiredProtoMarkerSite) String() string {
	return fmt.Sprintf("%s:%d: %s", filepath.Base(s.File), s.Line, s.Text)
}

// RawProtoMarkerSite locates one `// forge:*` marker in a source file.
type RawProtoMarkerSite struct {
	// File is the absolute path of the declaring .proto file.
	File string
	// Line is the 1-based line number of the marker.
	Line int
	// Text is the marker's source line, trimmed.
	Text string
}

// String renders the site as `<file>:<line>: <source line>`.
func (s RawProtoMarkerSite) String() string {
	return fmt.Sprintf("%s:%d: %s", filepath.Base(s.File), s.Line, s.Text)
}

// GeneratedFieldMarker is one `// forge:generated <expr>` marker the raw scan
// bound to a field.
type GeneratedFieldMarker struct {
	// Field is the proto field name the marker attached to.
	Field string
	// Expr is the SQL expression, verbatim ("" when the marker carried
	// none — birth refuses that).
	Expr string
	// Site locates the marker in its .proto file.
	Site RawProtoMarkerSite
}

// IsUnmanagedMarkerLine reports whether one raw proto source line is the
// `// forge:unmanaged` marker, by the same rule the raw scan applies — for
// line scanners outside this package that track their own declarations.
func IsUnmanagedMarkerLine(line string) bool {
	return unmanagedMarkerRE.MatchString(strings.TrimSpace(line))
}

// RawProtoService is one `service X {` block captured by the raw scan.
type RawProtoService struct {
	Name string
	// File is the absolute path of the declaring .proto file.
	File string
	// Unmanaged reports a `// forge:unmanaged` marker above the declaration
	// (ProtoMarkerUnmanaged) — the raw-source reading of
	// ServiceDef.Unmanaged, for passes that run before or without the
	// compiled descriptor.
	Unmanaged bool
	// BodyOpen / BodyClose are byte offsets into File: just after the
	// opening `{`, and at the matching `}`. Zero when the body was not
	// found (a single-line `service X {}` still records both).
	BodyOpen  int
	BodyClose int
}

// RawProtoRPC is one rpc declaration captured by the raw scan.
type RawProtoRPC struct {
	Name string
	// File is the absolute path of the declaring .proto file.
	File string
	// Service is the name of the service block that encloses the rpc, or ""
	// when the scan could not attribute it to one.
	Service string
	// Request is the request message type as written in the signature,
	// minus any `stream` keyword and leading dot ("ListOrdersRequest",
	// "shared.v1.PageQuery"). Not resolved against the declared types.
	Request string
	// Streaming reports a `stream` keyword on either side.
	Streaming bool
	// Custom reports a `// forge:custom` marker above the rpc
	// (ProtoMarkerCustom) — the raw-source reading of Method.Custom
	// (rawRPCMarkerComments). Generate refuses a project where the two
	// disagree, so after a successful generate they are the same answer.
	Custom bool
}

// rawRPCMarkerComments returns the comment text an rpc-level marker may sit
// in, with the `//` stripped so it has the descriptor path's shape: every
// comment line between the previous declaration and the rpc. Blank lines do
// not end the walk — this is where the author PUT the marker, which is the
// question generate's raw-source cross-check asks of the descriptor
// (protobuf may hand a block under one rpc to that rpc, or drop it). A
// trailing comment on the rpc line is not read: the descriptor path refuses a
// trailing marker, so it never makes an rpc custom. An rpc that does not
// start its line (`service S { rpc A(...) ...`) has no leading block:
// whatever sits above that line belongs to the declaration the line opens.
//
// start is the byte offset of the rpc signature within content.
func rawRPCMarkerComments(content string, start int) string {
	lineStart := strings.LastIndexByte(content[:start], '\n') + 1
	if strings.TrimSpace(content[lineStart:start]) != "" {
		return ""
	}
	var blocks []string
	above := strings.Split(content[:lineStart], "\n")
	for i := len(above) - 1; i >= 0; i-- {
		t := strings.TrimSpace(above[i])
		if t == "" {
			continue
		}
		text, ok := strings.CutPrefix(t, "//")
		if !ok {
			break
		}
		blocks = append([]string{strings.TrimLeft(text, "/")}, blocks...)
	}
	return strings.Join(blocks, "\n")
}

// RawProtoScan is the raw-truth view of one service's proto directory.
type RawProtoScan struct {
	// Package is the proto package shared by the scanned files.
	Package string
	// ServiceName / ServiceFile identify the first `service X {` block
	// found (the injection target for quintet completion). Empty when no
	// service block exists in the directory.
	ServiceName string
	ServiceFile string
	// Services are every service block found, in file+declaration order.
	Services []RawProtoService
	// Files are the absolute paths scanned, sorted.
	Files []string
	// Messages are the top-level messages, in file+declaration order.
	Messages []RawProtoMessage
	// Enums maps fully-qualified enum names (including message-nested
	// ones, "pkg.Order.Status") to their value names in declaration
	// order — the CHECK-constraint vocabulary for entity births.
	Enums map[string][]string
	// RPCs are every rpc declaration found across the files.
	RPCs []RawProtoRPC
}

// MessageByName returns the top-level message with the given relative
// name, if scanned.
func (s *RawProtoScan) MessageByName(name string) (RawProtoMessage, bool) {
	for _, m := range s.Messages {
		if m.Name == name {
			return m, true
		}
	}
	return RawProtoMessage{}, false
}

// ServiceUnmanaged reports whether the named service carries a
// `// forge:unmanaged` marker. False for a service the scan did not find.
func (s *RawProtoScan) ServiceUnmanaged(name string) bool {
	for _, svc := range s.Services {
		if svc.Name == name {
			return svc.Unmanaged
		}
	}
	return false
}

// RPCNames returns the set of rpc names declared across the scanned
// files.
func (s *RawProtoScan) RPCNames() map[string]bool {
	out := make(map[string]bool, len(s.RPCs))
	for _, r := range s.RPCs {
		out[r.Name] = true
	}
	return out
}

// DeclaresRPC reports whether any scanned file declares rpc <name>.
func (s *RawProtoScan) DeclaresRPC(name string) bool {
	for _, r := range s.RPCs {
		if r.Name == name {
			return true
		}
	}
	return false
}

// ScanRawProtoDir scans every .proto file under dir (recursively) and
// returns the raw-truth view. A missing directory returns an empty scan,
// not an error — callers treat "nothing there" as "nothing to do".
func ScanRawProtoDir(dir string) (*RawProtoScan, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".proto") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return &RawProtoScan{Enums: map[string][]string{}}, nil
		}
		return nil, err
	}
	return ScanRawProtoFiles(files)
}

// ScanRawProtoFiles scans exactly the given .proto files and returns the
// raw-truth view. The files are taken to share one proto package (the scan
// qualifies every type name with the first `package` it reads), so a caller
// covering a whole tree hands it one package directory's files at a time
// rather than the recursive set ScanRawProtoDir collects.
func ScanRawProtoFiles(files []string) (*RawProtoScan, error) { //nolint:gocognit,funlen // a hand-rolled proto lexer (252): one branch per declaration form and per marker comment. Decomposing it means writing a real tokenizer — a design change, not a lint fix.
	scan := &RawProtoScan{Enums: map[string][]string{}}
	files = append([]string(nil), files...)
	sort.Strings(files)
	scan.Files = files

	// Pass 1: structure — declarations, markers, enum values, rpcs, and
	// the raw (unclassified) field lines per top-level message.
	type rawField struct {
		label     string // "", "repeated", "optional"
		typeToken string // "string", "Order.Item", "map<string, int64>"
		mapKey    string
		mapValue  string
		name      string
		oneof     string
		options   string // trailing `[...]` inline options (buf.validate rules)
		readOnly  bool   // carries a `// forge:read-only` leading/trailing marker
		generated string // the `// forge:generated <expr>` expression, if any
	}
	type rawMessage struct {
		msg    *RawProtoMessage
		fields []rawField
	}
	var (
		messages         []*rawMessage
		declaredMessages = map[string]bool{} // relative dotted names ("Order", "Order.Item")
		declaredEnums    = map[string]bool{} // relative dotted names
	)

	for _, path := range files {
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", path, rerr)
		}
		content := stripBlockComments(string(raw))

		// Context stack: message/enum chains by relative dotted name.
		type ctx struct {
			kind string // "message", "enum", "service", "oneof", "block"
			name string // declared name for message/enum/service/oneof
		}
		var stack []ctx
		chainName := func() string {
			var parts []string
			for _, c := range stack {
				if c.kind == "message" || c.kind == "enum" {
					parts = append(parts, c.name)
				}
			}
			return strings.Join(parts, ".")
		}
		var currentMsg *rawMessage
		var carriedDecl *ctx // a decl whose opening brace sits on a later line
		// Three entity-marker flags, all pending until the next top-level
		// message consumes them. Any of them tablizes the message; the
		// behavior flags (append-only, soft-delete) additionally shape the
		// birth (RPC set / DB guard / deleted_at column).
		pendingMarker := false // // forge:entity (or any marker — see below)
		pendingAppendOnly := false
		pendingSoftDelete := false
		// SERVICE-level marker, pending until the next declaration exactly
		// like the entity markers above.
		pendingUnmanaged := false
		svcStartIdx := len(scan.Services)
		openService := -1 // index into scan.Services of the body being read
		// FIELD-level marker: a leading `// forge:read-only` comment line is
		// pending until the very next captured field consumes it (the trailing
		// inline spelling is read off the field's own statement instead).
		// Several marker lines may stack above one field (`// forge:read-only`
		// over `// forge:generated …`), so every pending site is kept: the
		// field consumes them all, and none is reported as unapplied.
		pendingFieldReadOnly := false
		var pendingFieldReadOnlyAt []int // byte offsets of the pending markers

		// Every `// forge:read-only` occurrence in this file, and which of
		// them a field actually consumed. Whatever is left over is a marker
		// the author wrote and forge did not honour — reported per message
		// so birth can refuse loudly instead of dropping it.
		readOnlySites := readOnlyFieldMarkerRE.FindAllStringIndex(content, -1)
		consumedReadOnly := make(map[int]bool, len(readOnlySites))
		msgStartIdx := len(messages)

		lines := strings.Split(content, "\n")
		lineStart := 0 // byte offset of `line` within content
		for _, line := range lines {
			// Content from this physical line to end-of-file. A field's inline
			// `[...]` options are read from here (not the single `line`) so a
			// braced protovalidate value that spans several lines —
			// `int32 m = 8 [(buf.validate.field).int32 = {`\n`  gte: 1`\n`  lte:
			// 12`\n`}];` — is captured whole: findInlineOptions scans across the
			// newlines to the matching `]`. The single `line` is still used for
			// the trailing `// forge:read-only` marker (which binds to the
			// field's own line).
			restFromLine := content[lineStart:]
			lineOffset := lineStart    // byte offset of `line` within content
			lineStart += len(line) + 1 // +1 for the split-consumed '\n'
			trimmedRaw := strings.TrimSpace(line)
			if trimmedRaw == "" {
				continue // blank line — a pending marker survives
			}
			if strings.HasPrefix(trimmedRaw, "//") {
				switch {
				case entityMarkerRE.MatchString(trimmedRaw):
					pendingMarker = true
				case appendOnlyMarkerRE.MatchString(trimmedRaw):
					pendingMarker, pendingAppendOnly = true, true
				case softDeleteMarkerRE.MatchString(trimmedRaw):
					pendingMarker, pendingSoftDelete = true, true
				case unmanagedMarkerRE.MatchString(trimmedRaw):
					pendingUnmanaged = true
				case readOnlyFieldMarkerRE.MatchString(trimmedRaw):
					// FIELD-level marker on its own line: attaches to the next
					// captured field, NOT to the message (never tablizes).
					pendingFieldReadOnly = true
					pendingFieldReadOnlyAt = append(pendingFieldReadOnlyAt,
						lineOffset+readOnlyFieldMarkerRE.FindStringIndex(line)[0])
				}
				continue // comment line — a pending marker survives
			}

			code := stripLineComment(line)

			// Inline bodies: split the comment-stripped code into
			// statement-grade segments (a break after every '{' and ';',
			// '}' isolated) so `message Widget { string id = 1; }` parses
			// exactly like its multi-line spelling. Conventionally
			// formatted files already end their lines at these
			// boundaries, so for them this loop degenerates to one
			// segment per line. Before this, one-liner bodies degraded to
			// "fields not captured" — and a captured-fieldless entity
			// births a COLUMN-LESS table, silently dropping every field.
			segOffset := 0
			for _, seg := range splitProtoStatements(code) {
				segStart := lineOffset + segOffset
				segOffset += len(seg)
				trimmed := strings.TrimSpace(seg)
				if trimmed == "" {
					continue
				}
				// Byte offset of `trimmed` within content — stripLineComment
				// and splitProtoStatements are both length-preserving over the
				// text they keep, so this indexes the raw file exactly.
				trimStart := segStart + strings.Index(seg, trimmed)

				// package line (top level).
				if len(stack) == 0 {
					if m := rawPackageRE.FindStringSubmatch(trimmed); m != nil {
						if scan.Package == "" {
							scan.Package = m[1]
						}
						pendingMarker, pendingAppendOnly, pendingSoftDelete = false, false, false
						pendingUnmanaged = false
						continue
					}
				}

				// Declaration on this line (message/enum/service/oneof)?
				// A decl whose brace opens on a LATER line is carried over.
				decl := carriedDecl
				carriedDecl = nil
				switch {
				case rawMessageDeclRE.MatchString(trimmed):
					name := rawMessageDeclRE.FindStringSubmatch(trimmed)[1]
					topLevel := len(stack) == 0
					full := name
					if c := chainName(); c != "" {
						full = c + "." + name
					}
					declaredMessages[full] = true
					decl = &ctx{kind: "message", name: name}
					if topLevel {
						// Nested messages register for type resolution only —
						// only top-level messages are markable/capturable.
						rm := &rawMessage{msg: &RawProtoMessage{
							Name:             name,
							File:             path,
							Marked:           pendingMarker, // Package is stamped in pass 2
							AppendOnly:       pendingAppendOnly,
							SoftDeleteMarked: pendingSoftDelete,
						}}
						messages = append(messages, rm)
						currentMsg = rm
					}
				case rawEnumDeclRE.MatchString(trimmed):
					name := rawEnumDeclRE.FindStringSubmatch(trimmed)[1]
					full := name
					if c := chainName(); c != "" {
						full = c + "." + name
					}
					declaredEnums[full] = true
					decl = &ctx{kind: "enum", name: name}
				case rawServiceDeclRE.MatchString(trimmed):
					name := rawServiceDeclRE.FindStringSubmatch(trimmed)[1]
					if scan.ServiceName == "" {
						scan.ServiceName = name
						scan.ServiceFile = path
					}
					if len(stack) == 0 {
						scan.Services = append(scan.Services, RawProtoService{
							Name: name, File: path, Unmanaged: pendingUnmanaged,
						})
					}
					decl = &ctx{kind: "service", name: name}
				case rawOneofDeclRE.MatchString(trimmed):
					decl = &ctx{kind: "oneof", name: rawOneofDeclRE.FindStringSubmatch(trimmed)[1]}
				}

				// Content lines (no declaration): fields inside a top-level
				// message (directly, or via a oneof directly inside it), and
				// enum values inside any enum.
				if decl == nil && len(stack) > 0 { //nolint:nestif // the lexer's content-line branch: enum values vs message fields vs oneof members, each with its own capture rule.
					top := stack[len(stack)-1]
					switch top.kind {
					case "enum":
						if m := rawEnumValueRE.FindStringSubmatch(trimmed); len(m) > 1 &&
							m[1] != "option" && m[1] != "reserved" {
							// chainName is the enum's dotted relative name
							// (including any containing message).
							rel := chainName()
							scan.Enums[rel] = append(scan.Enums[rel], m[1])
						}
					case "message", "oneof":
						// Fields belong to the innermost message; only capture
						// when that message is TOP-LEVEL (nested messages map to
						// JSONB downstream — their fields are never consumed).
						msgDepth := 0
						for _, c := range stack {
							if c.kind == "message" {
								msgDepth++
							}
						}
						// `reserved 5;` / `reserved 5 to 9;` claims numbers no
						// field may take — fold them into the message's high-water
						// mark so an appended managed field never lands on one.
						if currentMsg != nil && msgDepth == 1 && strings.HasPrefix(trimmed, "reserved ") {
							for _, n := range rawIntRE.FindAllString(trimmed, -1) {
								currentMsg.msg.MaxFieldNumber = maxInt(currentMsg.msg.MaxFieldNumber, atoiOr(n, 0))
							}
						}
						// Note the trailing spaces: `optional string x` must not
						// be swallowed by the `option` prefix.
						if currentMsg != nil && msgDepth == 1 &&
							!strings.HasPrefix(trimmed, "option ") &&
							!strings.HasPrefix(trimmed, "reserved ") &&
							!strings.HasPrefix(trimmed, "extensions ") {
							oneofName := ""
							if top.kind == "oneof" {
								oneofName = top.name
							}
							// The read-only-family markers LEADING this field
							// (pending) or TRAILING its declaration. Either way each
							// marker's site is recorded as consumed, and a
							// `forge:generated` one also yields its expression and
							// joins the message's site ledger.
							takeReadOnly := func(fieldName string) (readOnly bool, generated string) {
								sites := []int{}
								if pendingFieldReadOnly {
									sites = append(sites, pendingFieldReadOnlyAt...)
								}
								if off, ok := rawFieldTrailingReadOnly(restFromLine, fieldName); ok {
									sites = append(sites, lineOffset+off)
								}
								for _, at := range sites {
									consumedReadOnly[at] = true
									readOnly = true
									expr, isGenerated := GeneratedMarkerExpr(commentAt(content, at))
									if !isGenerated {
										continue
									}
									if generated == "" {
										generated = expr
									}
									currentMsg.msg.GeneratedMarkers = append(currentMsg.msg.GeneratedMarkers, GeneratedFieldMarker{
										Field: fieldName,
										Expr:  expr,
										Site:  RawProtoMarkerSite{File: path, Line: lineNumberAt(content, at), Text: sourceLineAt(content, at)},
									})
								}
								return readOnly, generated
							}
							if m := rawMapFieldRE.FindStringSubmatch(trimmed); m != nil {
								readOnly, generated := takeReadOnly(m[3])
								currentMsg.fields = append(currentMsg.fields, rawField{
									typeToken: "map", mapKey: m[1], mapValue: m[2], name: m[3], oneof: oneofName,
									readOnly: readOnly, generated: generated,
								})
								currentMsg.msg.MaxFieldNumber = maxInt(currentMsg.msg.MaxFieldNumber, atoiOr(m[4], 0))
								pendingFieldReadOnly, pendingFieldReadOnlyAt = false, nil
							} else if m := rawFieldRE.FindStringSubmatch(trimmed); m != nil {
								readOnly, generated := takeReadOnly(m[3])
								currentMsg.fields = append(currentMsg.fields, rawField{
									label: strings.TrimSpace(m[1]), typeToken: m[2], name: m[3], oneof: oneofName,
									// Read inline options off the ORIGINAL text (strings
									// intact): stripLineComment blanks quoted spans for
									// brace-safety, which would erase a `pattern = "..."`.
									// restFromLine (not `line`) so a braced value spanning
									// several physical lines is captured whole.
									options:   findInlineOptions(restFromLine, m[3]),
									readOnly:  readOnly,
									generated: generated,
								})
								currentMsg.msg.MaxFieldNumber = maxInt(currentMsg.msg.MaxFieldNumber, atoiOr(m[4], 0))
								pendingFieldReadOnly, pendingFieldReadOnlyAt = false, nil
							}
						}
					}
				}

				// Brace tracking: push the declaration ctx on its opening
				// brace, anonymous blocks otherwise; pop on close. A decl with
				// no brace on this line is carried to the next. The braces of
				// a TOP-LEVEL message additionally record its body span, so a
				// birth-time editor can append fields to it in place.
				declPushed := false
				for i, r := range trimmed {
					switch r {
					case '{':
						if decl != nil && !declPushed {
							if decl.kind == "message" && len(stack) == 0 && currentMsg != nil {
								currentMsg.msg.BodyOpen = trimStart + i + 1
							}
							if decl.kind == "service" && len(stack) == 0 && len(scan.Services) > svcStartIdx {
								openService = len(scan.Services) - 1
								scan.Services[openService].BodyOpen = trimStart + i + 1
							}
							stack = append(stack, *decl)
							declPushed = true
						} else {
							stack = append(stack, ctx{kind: "block"})
						}
					case '}':
						if len(stack) > 0 {
							popped := stack[len(stack)-1]
							stack = stack[:len(stack)-1]
							if popped.kind == "message" && len(stack) == 0 {
								if currentMsg != nil {
									currentMsg.msg.BodyClose = trimStart + i
								}
								currentMsg = nil
							}
							if popped.kind == "service" && len(stack) == 0 && openService >= 0 {
								scan.Services[openService].BodyClose = trimStart + i
								openService = -1
							}
						}
					}
				}
				if decl != nil && !declPushed {
					carriedDecl = decl
				}
				pendingMarker, pendingAppendOnly, pendingSoftDelete = false, false, false
				pendingUnmanaged = false
				// A pending field marker attaches to the immediately-next field
				// only: any non-field statement (a decl, an enum value) drops it.
				// Comment/blank lines `continue` above without reaching here, so
				// the marker still survives those between it and its field.
				pendingFieldReadOnly, pendingFieldReadOnlyAt = false, nil
			}
		}

		// Every read-only marker this file carries that no field took,
		// attributed to the top-level message whose body encloses it. Birth
		// refuses an entity carrying one: a marker forge cannot honour must
		// never be silently dropped.
		for _, site := range readOnlySites {
			if consumedReadOnly[site[0]] {
				continue
			}
			for _, rm := range messages[msgStartIdx:] {
				if site[0] < rm.msg.BodyOpen || site[0] > rm.msg.BodyClose {
					continue
				}
				rm.msg.UnappliedReadOnlyMarkers = append(rm.msg.UnappliedReadOnlyMarkers,
					RawProtoMarkerSite{File: path, Line: lineNumberAt(content, site[0]), Text: sourceLineAt(content, site[0])})
				break
			}
		}

		// Every RETIRED spelling this file carries, attributed the same way.
		// No consumed-set to consult: nothing can consume these, which is
		// precisely the failure being closed — a retired marker is inert, so
		// without this ledger the birth exits zero and the author's field
		// ships client-writable.
		for _, hit := range RetiredProtoMarkerSites(content) {
			for _, rm := range messages[msgStartIdx:] {
				if hit.Offset < rm.msg.BodyOpen || hit.Offset > rm.msg.BodyClose {
					continue
				}
				rm.msg.RetiredMarkers = append(rm.msg.RetiredMarkers, RetiredProtoMarkerSite{
					File:       path,
					Line:       lineNumberAt(content, hit.Offset),
					Text:       sourceLineAt(content, hit.Offset),
					Marker:     hit.Marker,
					ReplacedBy: hit.ReplacedBy,
				})
				break
			}
		}

		// rpcs: a single whole-file regex — rpc signatures are one-liners
		// in every proto forge scaffolds or documents.
		// Each is attributed to the service block whose body encloses it.
		for _, loc := range rawRPCRE.FindAllStringSubmatchIndex(content, -1) {
			req, resp := content[loc[4]:loc[5]], content[loc[6]:loc[7]]
			service := ""
			for _, svc := range scan.Services[svcStartIdx:] {
				if svc.BodyClose > 0 && loc[0] >= svc.BodyOpen && loc[0] < svc.BodyClose {
					service = svc.Name
					break
				}
			}
			scan.RPCs = append(scan.RPCs, RawProtoRPC{
				Name:      content[loc[2]:loc[3]],
				File:      path,
				Service:   service,
				Request:   rawRPCMessageType(req),
				Streaming: strings.Contains(req, "stream ") || strings.Contains(resp, "stream "),
				Custom:    CommentMarksCustom(rawRPCMarkerComments(content, loc[0])),
			})
		}
	}

	// Pass 2: classify the captured fields now that the full declared
	// type set (all files, nested names, enums) is known.
	pkg := scan.Package
	// Re-key enums by fully-qualified name.
	fqEnums := make(map[string][]string, len(scan.Enums))
	for rel, values := range scan.Enums {
		fqEnums[pkg+"."+rel] = values
	}
	scan.Enums = fqEnums

	classify := func(owner, token string) (kind, typeName string) {
		if IsProtoScalarKind(token) {
			return token, ""
		}
		if strings.HasPrefix(token, "google.protobuf.") {
			return "message", token
		}
		// Scoped resolution: a relative name inside message Owner first
		// tries Owner.Token (protoc's innermost-scope rule), then the
		// package-level name.
		candidates := []string{}
		if owner != "" && !strings.Contains(token, ".") {
			candidates = append(candidates, owner+"."+token)
		}
		candidates = append(candidates, token)
		for _, cand := range candidates {
			switch {
			case declaredEnums[cand]:
				return "enum", pkg + "." + cand
			case declaredMessages[cand]:
				return "message", pkg + "." + cand
			}
		}
		// Dotted reference whose ROOT is a declared same-package type but
		// whose full nested name wasn't registered (declared in a file the
		// walk missed): still same-package, kind unknowable → message (the
		// consumers' same-package handling applies).
		if first, _, ok := strings.Cut(token, "."); ok && (declaredMessages[first] || declaredEnums[first]) {
			return "message", pkg + "." + token
		}
		if strings.Contains(token, ".") {
			// Cross-package reference — carried fully qualified; consumers
			// TODO-carry these (the raw scan cannot resolve foreign kinds).
			return "message", token
		}
		// Unresolvable bare name: treat as a same-package message so the
		// consumer's same-package handling (JSONB / TODO) applies.
		return "message", pkg + "." + token
	}

	for _, rm := range messages {
		rm.msg.Package = pkg
		for _, f := range rm.fields {
			def := SchemaFieldDef{Name: f.name, Oneof: f.oneof}
			switch {
			case f.typeToken == "map":
				def.Kind = "map"
				def.MapKeyKind = f.mapKey
				def.MapValueKind, def.MapValueTypeName = classify(rm.msg.Name, f.mapValue)
			default:
				def.Kind, def.TypeName = classify(rm.msg.Name, f.typeToken)
				def.Repeated = f.label == "repeated"
				def.Optional = f.label == "optional"
			}
			// protovalidate rules on the raw field line — the born migration
			// for a brand-new `// forge:entity` message reads them from here.
			def.Validate = ParseRawValidateOptions(f.options)
			// …and their authored SPELLING, so a born request message that
			// FLATTENS this field (Create<Entity>Request) re-declares the
			// same rules and the wire interceptor enforces them there too.
			def.ValidateOptions = ValidateFieldOptions(f.options)
			// `// forge:read-only` — carried so the born Create/Update
			// request messages omit this non-client-writable field.
			def.ReadOnly = f.readOnly
			// `// forge:generated <expr>` — the born column is
			// GENERATED ALWAYS AS (<expr>) STORED.
			def.Generated = f.generated
			rm.msg.Fields = append(rm.msg.Fields, def)
		}
		scan.Messages = append(scan.Messages, *rm.msg)
	}

	return scan, nil
}

var (
	rawPackageRE     = regexp.MustCompile(`^package\s+([\w.]+)\s*;`)
	rawMessageDeclRE = regexp.MustCompile(`^message\s+(\w+)\s*\{?`)
	rawEnumDeclRE    = regexp.MustCompile(`^enum\s+(\w+)\s*\{?`)
	rawServiceDeclRE = regexp.MustCompile(`^service\s+(\w+)\s*\{?`)
	rawOneofDeclRE   = regexp.MustCompile(`^oneof\s+(\w+)\s*\{?`)
	rawEnumValueRE   = regexp.MustCompile(`^(\w+)\s*=\s*-?\d+`)
	rawFieldRE       = regexp.MustCompile(`^(repeated\s+|optional\s+)?([\w.]+)\s+(\w+)\s*=\s*(\d+)`)
	rawMapFieldRE    = regexp.MustCompile(`^map\s*<\s*(\w+)\s*,\s*([\w.]+)\s*>\s+(\w+)\s*=\s*(\d+)`)
	rawRPCRE         = regexp.MustCompile(`\brpc\s+(\w+)\s*\(([^)]*)\)\s*returns\s*\(([^)]*)\)`)
	rawIntRE         = regexp.MustCompile(`\d+`)
)

// rawRPCMessageType normalizes one side of an rpc signature — the text
// between the parens — to the bare type token: `stream .pkg.Foo ` → "pkg.Foo".
func rawRPCMessageType(sig string) string {
	t := strings.TrimSpace(sig)
	t = strings.TrimSpace(strings.TrimPrefix(t, "stream "))
	return strings.TrimPrefix(t, ".")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// lineNumberAt returns the 1-based line number of byte offset `off`.
func lineNumberAt(content string, off int) int {
	if off > len(content) {
		off = len(content)
	}
	return strings.Count(content[:off], "\n") + 1
}

// commentAt returns content from byte offset `off` to the end of that line —
// the comment a marker site starts, with its leading `//`.
func commentAt(content string, off int) string {
	if off > len(content) {
		return ""
	}
	rest := content[off:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		return rest[:nl]
	}
	return rest
}

// sourceLineAt returns the trimmed source line containing byte offset `off`.
func sourceLineAt(content string, off int) string {
	if off > len(content) {
		off = len(content)
	}
	start := strings.LastIndexByte(content[:off], '\n') + 1
	end := strings.IndexByte(content[off:], '\n')
	if end < 0 {
		return strings.TrimSpace(content[start:])
	}
	return strings.TrimSpace(content[start : off+end])
}

// rawFieldTrailingReadOnly reports whether the field named `field` carries
// a TRAILING `// forge:read-only` comment (either spelling), returning the
// byte offset of that comment within `text` (the file content from the
// field's own line to EOF, strings and comments intact — same input
// findInlineOptions reads).
//
// A trailing comment binds to the field whose DECLARATION it follows, so
// the scan walks from `<field> = <number>` to the `;` that terminates the
// declaration — across newlines, through the inline `[...]` options block
// and any braced protovalidate value inside it — and then accepts a `//`
// comment that follows before the line ends. Two fields on one inline-body
// line therefore still bind correctly: the first field's statement ends at
// its own `;`, and what follows is the second field, not a comment.
//
// The previous reader worked on the field's single physical line and
// rejected the marker whenever any `<name> = <number>` sat between field
// and comment. Every inline options bracket contains one:
//
//	int64 unit_price_cents = 5 [(buf.validate.field).int64.gte = 0]; // forge:read-only
//
// `gte = 0` read as "a later field declaration", so the marker was
// discarded — silently. The author believed the field was off the write
// surface and it shipped on the born Create request. The correlation was
// exact across a twelve-field app: every field with a bracket kept, every
// field without one stripped.
func rawFieldTrailingReadOnly(text, field string) (int, bool) {
	fre := regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `\s*=\s*\d+`)
	loc := fre.FindStringIndex(text)
	if loc == nil {
		return 0, false
	}
	semi := rawStatementEnd(text, loc[1])
	if semi < 0 {
		return 0, false
	}
	i := semi + 1
	for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\r') {
		i++
	}
	if i+1 >= len(text) || text[i] != '/' || text[i+1] != '/' {
		return 0, false
	}
	comment := text[i:]
	if nl := strings.IndexByte(comment, '\n'); nl >= 0 {
		comment = comment[:nl]
	}
	if !readOnlyFieldMarkerRE.MatchString(comment) {
		return 0, false
	}
	return i, true
}

// rawStatementEnd returns the index of the `;` terminating the field
// declaration that starts at `from`, or -1 when there is none before the
// enclosing message closes. Bracket/brace groups (the inline options and
// their braced values) are skipped whole, quoted spans are skipped, and a
// `//` comment inside the declaration runs to end of line.
func rawStatementEnd(text string, from int) int {
	depth := 0
	for i := from; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '"':
			for i++; i < len(text); i++ {
				if text[i] == '\\' {
					i++
					continue
				}
				if text[i] == '"' {
					break
				}
			}
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == '[', c == '{':
			depth++
		case c == ']':
			depth--
		case c == '}':
			if depth == 0 {
				return -1 // the message closed before the field terminated
			}
			depth--
		case c == ';' && depth == 0:
			return i
		}
	}
	return -1
}

// findInlineOptions returns the `[...]` inline-options substring for the
// field named `field`, reading ACROSS physical lines (`text` is the file
// content from the field's line to EOF, strings still intact). It locates
// `<field> = <number>` and captures the following bracket group, scanning to
// the matching `]`. A protovalidate value is routinely braced and MULTI-LINE
// — `int32 m = 8 [(buf.validate.field).int32 = {`\n`  gte: 1`\n`  lte: 12`\n
// `}];` — whose `[...]` block does not close on the field's own line; reading
// a single line dropped every such rule (the birthed migration then carried
// NO CHECK). While scanning: quoted spans are skipped so a `pattern = "...]..."`
// bracket inside a string doesn't truncate the capture; `//` line comments are
// dropped (their braces/brackets must not unbalance the scan); and newlines
// collapse to spaces so the returned text parses identically to the single-line
// spelling. A `;` before the `[` means the options belong to a LATER field on
// the same (inline-body) line, so none are returned.
func findInlineOptions(text, field string) string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `\s*=\s*\d+`)
	loc := re.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	rest := text[loc[1]:]
	open := strings.IndexByte(rest, '[')
	if open < 0 {
		return ""
	}
	if semi := strings.IndexByte(rest[:open], ';'); semi >= 0 {
		return ""
	}
	var b strings.Builder
	depth := 0
	inStr := false
	for i := open; i < len(rest); i++ {
		c := rest[i]
		if inStr {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(rest) {
				b.WriteByte(rest[i+1])
				i++
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		// Drop a `//` line comment to end of line — a comment may embed
		// braces/brackets that would otherwise unbalance the bracket scan.
		if c == '/' && i+1 < len(rest) && rest[i+1] == '/' {
			for i < len(rest) && rest[i] != '\n' {
				i++
			}
			b.WriteByte(' ') // the dropped comment (and its newline) becomes a space
			continue
		}
		if c == '\n' {
			b.WriteByte(' ')
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
		}
		b.WriteByte(c)
		if c == ']' && depth == 0 {
			return b.String()
		}
	}
	return ""
}

// splitProtoStatements splits one comment-stripped code line into
// statement-grade segments: a break after every '{' and ';', with '}'
// isolated on its own segment. Inline bodies — `message Widget {
// string id = 1; }` — thereby parse identically to their multi-line
// spelling; conventionally formatted files already end their lines at
// these boundaries, so for them this returns the line unchanged.
// Braces/semicolons inside string literals are no concern here: the
// caller strips comments AND blanks quoted spans (stripLineComment)
// before splitting.
func splitProtoStatements(code string) []string {
	var (
		segs []string
		cur  strings.Builder
	)
	flush := func() {
		if cur.Len() > 0 {
			segs = append(segs, cur.String())
			cur.Reset()
		}
	}
	for _, r := range code {
		switch r {
		case '{', ';':
			cur.WriteRune(r)
			flush()
		case '}':
			flush()
			cur.WriteRune(r)
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return segs
}

// stripBlockComments blanks /* ... */ spans, preserving newlines so
// line-oriented scanning keeps its positions.
func stripBlockComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inBlock := false
	for i := 0; i < len(s); i++ {
		if inBlock {
			if s[i] == '*' && i+1 < len(s) && s[i+1] == '/' {
				inBlock = false
				i++
				b.WriteString("  ")
				continue
			}
			if s[i] == '\n' {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
			continue
		}
		if s[i] == '/' && i+1 < len(s) && s[i+1] == '*' {
			inBlock = true
			i++
			b.WriteString("  ")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// stripLineComment removes a trailing // comment, respecting double
// quotes (option strings), and blanks quoted spans so brace counting
// never sees string content.
func stripLineComment(line string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inString {
			if c == '\\' && i+1 < len(line) {
				b.WriteString("  ")
				i++
				continue
			}
			if c == '"' {
				inString = false
				b.WriteByte('"')
				continue
			}
			b.WriteByte(' ')
			continue
		}
		if c == '"' {
			inString = true
			b.WriteByte('"')
			continue
		}
		if c == '/' && i+1 < len(line) && line[i+1] == '/' {
			break
		}
		b.WriteByte(c)
	}
	return b.String()
}
