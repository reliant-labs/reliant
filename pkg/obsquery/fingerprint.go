package obsquery

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Server error fingerprint, algorithm sfp1: which ERROR log records are the
// same issue.
//
// A server error event is a log record at ERROR or above from a service that
// is not a browser (ServerErrorFilterSQL). forge/pkg/observe stamps every such
// record with its call site and the kind of failure it carries; sfp1 hashes
// those, never a line number (lines move between releases and would split an
// issue):
//
//	group_key = error.group, when the record names one (observe.ErrorGroup)
//	          | join(US,
//	              code.function.name, or ScopeName when absent,
//	              procedure            (rpc-scoped records; '' otherwise),
//	              norm(Body)           (the message template),
//	              exception.type,
//	              norm(exception.message) when exception.type is generic
//	                (errors.New, fmt.Errorf, or none), '' otherwise)
//	sfp1      = lowercase hex of the first 8 bytes of
//	            SHA-256("sfp1" US ServiceName US group_key)
//
// US is the ASCII unit separator, 0x1F. norm is MessageNormSQL /
// NormalizeMessage: the browser pipeline's MessageNorm masks.
//
// The algorithm exists exactly once, here, in two spellings that must agree:
// ServerFingerprintSQL, which the control plane's ingest view and forge's
// local query-time grouping both evaluate, and ServerFingerprint, the Go
// reference. The integration corpus test runs both over the same records on a
// real ClickHouse. Changing either output is a new algorithm (sfp2, a new
// ServerFingerprintVersion), never an edit to this one: stored issues are keyed
// by it.

// ServerFingerprintVersion is the fp_version an sfp1 fingerprint is stored with.
const ServerFingerprintVersion = 1

// Log attribute keys sfp1 reads. They are the keys forge/pkg/observe writes on
// every ERROR record (error_inputs.go) and the procedure key its rpc
// interceptor logs; the test pins that they agree.
const (
	LogKeyCodeFunction     = "code.function.name"
	LogKeyCodeFile         = "code.file.path"
	LogKeyProcedure        = "procedure"
	LogKeyExceptionType    = "exception.type"
	LogKeyExceptionMessage = "exception.message"
	LogKeyErrorGroup       = "error.group"
)

// ServerErrorFilterSQL selects server error events from otel_logs: ERROR or
// above (SeverityNumber 17+), and not from a browser session.
const ServerErrorFilterSQL = `SeverityNumber >= 17 AND ResourceAttributes['rum.sessionId'] = ''`

// The MessageNorm mask chain, split around the expression it normalizes. The
// masks, in order: URLs, UUIDs, quoted strings, hex runs of 8+, integers; first
// line only; 200 bytes. It is the browser pipeline's chain character for
// character (the control plane's mv_error_events).
const (
	msgNormPrefix = `substring(replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(splitByChar('\n', `
	msgNormSuffix = `)[1], ` +
		`'[a-zA-Z][a-zA-Z0-9+.-]*://[^\\s\\x27")]+', '<url>'), ` +
		`'[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}', '<uuid>'), ` +
		`'\\x27[^\\x27]*\\x27|"[^"]*"', '<str>'), ` +
		`'\\b[0-9a-fA-F]{8,}\\b', '<hex>'), ` +
		`'\\b[0-9]+\\b', '<n>'), 1, 200)`
)

// MessageNormSQL is the ClickHouse expression that normalizes the String
// expression expr the way NormalizeMessage does. expr is spliced verbatim: pass
// a column or a constant expression, never caller input.
func MessageNormSQL(expr string) string { return msgNormPrefix + expr + msgNormSuffix }

// Expressions over otel_logs that sfp1 and the issue display read.
const (
	sqlFunction = `if(LogAttributes['code.function.name'] != '', LogAttributes['code.function.name'], ScopeName)`
	sqlGeneric  = `LogAttributes['exception.type'] IN ('*errors.errorString', '*fmt.wrapError', '*fmt.wrapErrors', '')`
	sqlBodyNorm = msgNormPrefix + `Body` + msgNormSuffix
	sqlExcNorm  = msgNormPrefix + `LogAttributes['exception.message']` + msgNormSuffix
)

// ServerFingerprintSQL is sfp1 as a ClickHouse String expression over an
// otel_logs row (ServiceName, ScopeName, Body, LogAttributes). Evaluate it only
// on rows ServerErrorFilterSQL selects.
const ServerFingerprintSQL = `lower(hex(substring(SHA256(concat('sfp1', '\x1f', ServiceName, '\x1f', ` +
	`if(LogAttributes['error.group'] != '', LogAttributes['error.group'], concatWithSeparator('\x1f', ` +
	sqlFunction + `, ` +
	`LogAttributes['procedure'], ` +
	sqlBodyNorm + `, ` +
	`LogAttributes['exception.type'], ` +
	`if(` + sqlGeneric + `, ` + sqlExcNorm + `, '')` +
	`)))), 1, 8)))`

// ServerTitleSQL is an issue's title: "exception.type: norm(Body)", or
// norm(Body) alone when the record carries no error.
const ServerTitleSQL = `if(LogAttributes['exception.type'] != '', concat(LogAttributes['exception.type'], ': ', ` + sqlBodyNorm + `), ` + sqlBodyNorm + `)`

// ServerCulpritSQL is where an issue was logged: "file in function", or the
// function alone (ScopeName for a record with no source location).
const ServerCulpritSQL = `if(LogAttributes['code.file.path'] != '', concat(LogAttributes['code.file.path'], ' in ', ` + sqlFunction + `), ` + sqlFunction + `)`

// ServerErrorRecord is the part of an otel_logs row sfp1 reads.
type ServerErrorRecord struct {
	ServiceName string
	ScopeName   string
	Body        string
	// Attributes are the row's LogAttributes.
	Attributes map[string]string
}

// genericExceptionTypes carry nothing but their message, so the message is
// what tells two of them apart.
var genericExceptionTypes = map[string]bool{
	"*errors.errorString": true,
	"*fmt.wrapError":      true,
	"*fmt.wrapErrors":     true,
	"":                    true,
}

func (r ServerErrorRecord) function() string {
	if f := r.Attributes[LogKeyCodeFunction]; f != "" {
		return f
	}
	return r.ScopeName
}

// ServerFingerprint is the Go reference for ServerFingerprintSQL.
func ServerFingerprint(r ServerErrorRecord) string {
	key := r.Attributes[LogKeyErrorGroup]
	if key == "" {
		typ := r.Attributes[LogKeyExceptionType]
		msg := ""
		if genericExceptionTypes[typ] {
			msg = NormalizeMessage(r.Attributes[LogKeyExceptionMessage])
		}
		key = strings.Join([]string{r.function(), r.Attributes[LogKeyProcedure], NormalizeMessage(r.Body), typ, msg}, "\x1f")
	}
	sum := sha256.Sum256([]byte("sfp1\x1f" + r.ServiceName + "\x1f" + key))
	return hex.EncodeToString(sum[:8])
}

// ServerTitle is the Go reference for ServerTitleSQL.
func ServerTitle(r ServerErrorRecord) string {
	norm := NormalizeMessage(r.Body)
	if typ := r.Attributes[LogKeyExceptionType]; typ != "" {
		return typ + ": " + norm
	}
	return norm
}

// ServerCulprit is the Go reference for ServerCulpritSQL.
func ServerCulprit(r ServerErrorRecord) string {
	if file := r.Attributes[LogKeyCodeFile]; file != "" {
		return file + " in " + r.function()
	}
	return r.function()
}

// The MessageNorm masks, in MessageNormSQL's order.
var messageMasks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s'")]+`), "<url>"},
	{regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`), "<uuid>"},
	{regexp.MustCompile(`'[^']*'|"[^"]*"`), "<str>"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`), "<hex>"},
	{regexp.MustCompile(`\b[0-9]+\b`), "<n>"},
}

// maxMessageNorm is MessageNorm's length, in bytes (ClickHouse substring).
const maxMessageNorm = 200

// NormalizeMessage is the Go reference for MessageNormSQL: the first line of s
// with URLs, UUIDs, quoted strings, long hex runs and integers masked, cut to
// 200 bytes.
func NormalizeMessage(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	for _, m := range messageMasks {
		s = m.re.ReplaceAllLiteralString(s, m.with)
	}
	if len(s) > maxMessageNorm {
		s = s[:maxMessageNorm]
	}
	return s
}
