package obsquery

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Server error fingerprint, algorithms sfp1 and sfp2: which ERROR log records
// are the same issue.
//
// A server error event is a log record at ERROR or above from a service that
// is not a browser (ServerErrorFilterSQL). forge/pkg/observe stamps every such
// record with its call site and the kind of failure it carries; the
// fingerprint hashes those, never a line number (lines move between releases
// and would split an issue):
//
//	group_key = error.group, when the record names one (observe.ErrorGroup)
//	          | join(US,
//	              code.function.name, or ScopeName when absent (the SDK sets
//	              it to the innermost application frame of
//	              exception.stacktrace when the record carries a stack, else
//	              to the log call site),
//	              procedure            (rpc-scoped records; '' otherwise),
//	              norm(Body)           (the message template),
//	              exception.type,
//	              norm(exception.message) when exception.type is generic
//	                (errors.New, fmt.Errorf, or none), '' otherwise)
//	            [+ US class(error)     sfp2 only; see below]
//	sfp1      = lowercase hex of the first 8 bytes of
//	            SHA-256("sfp1" US ServiceName US group_key)
//
// US is the ASCII unit separator, 0x1F. norm is MessageNormSQL /
// NormalizeMessage: the browser pipeline's MessageNorm masks.
//
// # sfp2: the cause refines the group
//
// A log line that carries its cause in the `error` attribute and no
// exception.type (a string, or a logger outside forge/pkg/observe) groups by its
// message template alone under sfp1, so "Transaction failed" lumps UTF-8
// corruption, serialization failures, prepared-statement clashes and timeouts
// into one issue. sfp2 folds the error CLASS into the key of exactly those
// records (RefinedByErrorClass):
//
//	class(error) = [type " "] "SQLSTATE <code>"  when the error carries
//	                                   "(SQLSTATE <code>)" (a pgconn.PgError,
//	                                   however it is wrapped)
//	             | type                when the error starts with a Go type name
//	                                   ("*pgconn.PgError: ...") that is not one of the
//	                                   message-only types (errors.New, fmt.Errorf)
//	             | norm(error)         otherwise
//
// and hashes it under the prefix "sfp2" instead of "sfp1". The class refines the
// call site; it never replaces it: a stack-bearing error still groups by the
// frame the SDK stamped into code.function.name (svcerr's origin frame), and
// the class only splits what that frame lumped.
//
// # Versioning: a compatible refinement, not a re-key
//
// An issue's identity is (fingerprint, fp_version), so an algorithm that changed
// every fingerprint would silently re-key every issue: resolved ones would
// reopen, ignored ones would nag, a regression would look new. sfp2 is therefore
// a REFINEMENT of sfp1: for every record it does not refine (no `error`
// attribute, or an exception.type, or an error.group), its output is the sfp1
// fingerprint, byte for byte. Only the records sfp1 under-grouped get new
// fingerprints, which is the point; the issue they used to share is left as it
// is (it stops receiving those records and ages out, or a person resolves it),
// never rewritten, because it conflated different causes by definition.
//
// What a store keyed by (fingerprint, fp_version) does at the bump: it looks an
// issue up under every version in [ServerFingerprintCompatibleFrom,
// ServerFingerprintVersion], takes the highest, and creates new issues under the
// current version. It does NOT re-stamp the issues it finds: a rollout runs the
// old and the new version side by side, and an old worker looking for (fp, 1)
// after a re-stamp to 2 would not find the issue and would file a twin. Where
// both exist anyway (an old worker filed one while a new one ran) the lower is
// merged into the higher, state carried. The control plane's issues worker does
// exactly this (internal/workers/issues Store.GetIssueCompatible). A future sfp3
// follows the same rule: change only what it must, keep every other record's
// output byte-identical, bump ServerFingerprintVersion, keep
// ServerFingerprintCompatibleFrom while that holds, and extend the frozen vectors
// in the tests.
//
// The algorithm exists exactly once, here, in two spellings that must agree:
// ServerFingerprintSQL, which the control plane's ingest view and forge's
// local query-time grouping both evaluate, and ServerFingerprint, the Go
// reference. The integration corpus test runs both over the same records on a
// real ClickHouse. A change to either output that touches a record sfp2 does
// not refine is a re-key and is refused by TestServerFingerprintIsStable.

// ServerFingerprintVersion is the fp_version of the algorithm in force (sfp2):
// stamped on every newly fingerprinted row, and the version an issue is keyed
// by.
const ServerFingerprintVersion = 2

// ServerFingerprintCompatibleFrom is the oldest fp_version whose fingerprints
// still name the same issues under ServerFingerprintVersion (see the versioning
// note above): an issue stamped anywhere in [ServerFingerprintCompatibleFrom,
// ServerFingerprintVersion] is found by a lookup of its fingerprint.
const ServerFingerprintCompatibleFrom = 1

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
	LogKeyIssueSource      = "issue.source"
	// LogKeyError is the attribute a logger puts a failure's cause in when it
	// writes it as a string (slog's conventional "error" key).
	LogKeyError = "error"
)

// ServerErrorFilterSQL selects server error events from otel_logs: ERROR or
// above (SeverityNumber 17+), and not from a browser session.
const ServerErrorFilterSQL = `SeverityNumber >= 17 AND ResourceAttributes['rum.sessionId'] = ''`

// Issue sources. A record carrying issue.source (observe.IssueSource) is an
// issue event of that source at ANY level: an agent's bug report, say, which
// must be grouped and triaged like an error without counting as one. Errors
// carry no source (""). Every issue has exactly one source, and sfp1
// fingerprints both kinds the same way (error.group is how a source pins its
// grouping).
const (
	// IssueEventFilterSQL selects every record an issue is built from: server
	// errors, and server records of any level that carry a source. The
	// control plane's ingest view filters on it.
	IssueEventFilterSQL = `ResourceAttributes['rum.sessionId'] = '' AND (SeverityNumber >= 17 OR LogAttributes['issue.source'] != '')`
	// IssueSourceSQL is a record's issue source, "" for an error.
	IssueSourceSQL = `LogAttributes['issue.source']`
)

// validSource is the shape of an issue source name.
var validSource = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// ValidIssueSource reports whether s can name an issue source: 1-64 of
// lowercase letters, digits, '_', '.' and '-', starting with a letter or digit.
func ValidIssueSource(s string) bool { return validSource.MatchString(s) }

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

// Expressions over otel_logs that the fingerprints and the issue display read.
const (
	sqlFunction = `if(LogAttributes['code.function.name'] != '', LogAttributes['code.function.name'], ScopeName)`
	sqlGeneric  = `LogAttributes['exception.type'] IN ('*errors.errorString', '*fmt.wrapError', '*fmt.wrapErrors', '')`
	sqlBodyNorm = msgNormPrefix + `Body` + msgNormSuffix
	sqlExcNorm  = msgNormPrefix + `LogAttributes['exception.message']` + msgNormSuffix

	// sqlRefined selects the records sfp2 refines: a cause in `error`, and
	// nothing else that already says what kind of failure it is.
	sqlRefined = `(LogAttributes['error.group'] = '' AND LogAttributes['exception.type'] = '' AND LogAttributes['error'] != '')`
	// sqlGroupKey is sfp1's group_key for a record with no error.group.
	sqlGroupKey = `concatWithSeparator('\x1f', ` +
		sqlFunction + `, ` +
		`LogAttributes['procedure'], ` +
		sqlBodyNorm + `, ` +
		`LogAttributes['exception.type'], ` +
		`if(` + sqlGeneric + `, ` + sqlExcNorm + `, ''))`
)

// errClassSQL is ErrorClass over the String expression src: a "(SQLSTATE nnnnn)"
// code, with the Go type name ("*pgconn.PgError: ...") that leads the text when
// there is one; else that type name; else the normalized text. src is spliced
// verbatim.
func errClassSQL(src string) string {
	typ := `extract(` + src + `, '^(\\*?[a-z][a-z0-9_]*\\.[A-Za-z][A-Za-z0-9_]*): ')`
	state := `extract(` + src + `, '\\(SQLSTATE ([0-9A-Z]{5})\\)')`
	return `multiIf(` +
		state + ` != '', concat(if(` + typ + ` != '', concat(` + typ + `, ' '), ''), 'SQLSTATE ', ` + state + `), ` +
		typ + ` != '' AND ` + typ + ` NOT IN ('*errors.errorString', '*fmt.wrapError', '*fmt.wrapErrors'), ` + typ + `, ` +
		MessageNormSQL(src) + `)`
}

// errCauseSQL is ServerCause over the String expression src.
func errCauseSQL(src string) string {
	state := `extract(` + src + `, '\\(SQLSTATE ([0-9A-Z]{5})\\)')`
	refused := `extract(` + src + `, 'dial tcp ([^\\s]+): connect: connection refused')`
	return `multiIf(` +
		state + ` != '', concat('SQLSTATE ', ` + state + `), ` +
		refused + ` != '', concat('connection refused ', ` + refused + `), ` +
		src + ` != '', ` + errClassSQL(src) + `, '')`
}

var (
	// sqlErrClass is the error class of a record's `error` attribute.
	sqlErrClass = errClassSQL(`LogAttributes['error']`)

	// ServerFingerprintSQL is the server fingerprint (sfp1, refined to sfp2 for
	// the records RefinedByErrorClass selects) as a ClickHouse String expression
	// over an otel_logs row (ServiceName, ScopeName, Body, LogAttributes).
	// Evaluate it only on rows ServerErrorFilterSQL selects.
	ServerFingerprintSQL = `lower(hex(substring(SHA256(concat(` +
		`if(` + sqlRefined + `, 'sfp2', 'sfp1'), '\x1f', ServiceName, '\x1f', ` +
		`if(LogAttributes['error.group'] != '', LogAttributes['error.group'], ` + sqlGroupKey + `), ` +
		`if(` + sqlRefined + `, concat('\x1f', ` + sqlErrClass + `), '')` +
		`)), 1, 8)))`

	// ServerTitleSQL is an issue's title: "exception.type: norm(Body)", or
	// norm(Body) alone when the record carries no error. A record sfp2 refines
	// has no exception.type, and its error class stands in for it, so the issues
	// one message template splits into read apart.
	ServerTitleSQL = `if(LogAttributes['exception.type'] != '', concat(LogAttributes['exception.type'], ': ', ` + sqlBodyNorm + `), ` +
		`if(` + sqlRefined + `, concat(` + sqlErrClass + `, ': ', ` + sqlBodyNorm + `), ` + sqlBodyNorm + `))`

	// ServerCauseSQL is a record's incident cause: the ClickHouse spelling of
	// ServerCause, over the `error` attribute, else the SDK's exception.message.
	ServerCauseSQL = errCauseSQL(`if(LogAttributes['error'] != '', LogAttributes['error'], LogAttributes['exception.message'])`)

	// ServerEventCauseSQL is ServerCauseSQL over a server_error_events row, which
	// keeps the two attributes in its Attributes map (the control plane's ingest
	// view, tenant schema v9). A row stored before that keeps neither and has no
	// cause: an issue with no cause belongs to no incident.
	ServerEventCauseSQL = errCauseSQL(`if(Attributes['error'] != '', Attributes['error'], Attributes['exception.message'])`)
)

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

// RefinedByErrorClass reports whether sfp2 folds the error class into the
// fingerprint of r: it carries a cause in the `error` attribute and neither an
// error.group nor an exception.type. A record the SDK typed keeps its sfp1
// fingerprint exactly: the type is the class.
func (r ServerErrorRecord) RefinedByErrorClass() bool {
	return r.Attributes[LogKeyErrorGroup] == "" && r.Attributes[LogKeyExceptionType] == "" && r.Attributes[LogKeyError] != ""
}

// ServerFingerprint is the Go reference for ServerFingerprintSQL.
func ServerFingerprint(r ServerErrorRecord) string {
	algo := "sfp1"
	key := r.Attributes[LogKeyErrorGroup]
	if key == "" {
		typ := r.Attributes[LogKeyExceptionType]
		msg := ""
		if genericExceptionTypes[typ] {
			msg = NormalizeMessage(r.Attributes[LogKeyExceptionMessage])
		}
		key = strings.Join([]string{r.function(), r.Attributes[LogKeyProcedure], NormalizeMessage(r.Body), typ, msg}, "\x1f")
	}
	if r.RefinedByErrorClass() {
		algo = "sfp2"
		key += "\x1f" + ErrorClass(r.Attributes[LogKeyError])
	}
	sum := sha256.Sum256([]byte(algo + "\x1f" + r.ServiceName + "\x1f" + key))
	return hex.EncodeToString(sum[:8])
}

// ServerTitle is the Go reference for ServerTitleSQL.
func ServerTitle(r ServerErrorRecord) string {
	norm := NormalizeMessage(r.Body)
	if typ := r.Attributes[LogKeyExceptionType]; typ != "" {
		return typ + ": " + norm
	}
	if r.RefinedByErrorClass() {
		return ErrorClass(r.Attributes[LogKeyError]) + ": " + norm
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

var (
	errTypePrefix = regexp.MustCompile(`^(\*?[a-z][a-z0-9_]*\.[A-Za-z][A-Za-z0-9_]*): `)
	errSQLState   = regexp.MustCompile(`\(SQLSTATE ([0-9A-Z]{5})\)`)
)

// ErrorClass is the Go reference for ErrorClassSQL: what kind of failure the
// string in a record's `error` attribute names, stable across the ids, values
// and timings its message carries. A SQLSTATE code (with the Go type that
// prefixes it, when there is one) beats a bare type name, which beats the
// message run through NormalizeMessage.
func ErrorClass(err string) string {
	typ := ""
	if m := errTypePrefix.FindStringSubmatch(err); m != nil {
		typ = m[1]
	}
	if m := errSQLState.FindStringSubmatch(err); m != nil {
		if typ != "" {
			return typ + " SQLSTATE " + m[1]
		}
		return "SQLSTATE " + m[1]
	}
	if typ != "" && !genericExceptionTypes[typ] {
		return typ
	}
	return NormalizeMessage(err)
}

var errRefused = regexp.MustCompile(`dial tcp (\S+): connect: connection refused`)

// ServerCause is the Go reference for ServerCauseSQL: what an incident shares
// across the callers it breaks, read from the `error` attribute, else the SDK's
// exception.message. The SQLSTATE the text carries; else the target a dial was
// refused at; else its error class. "" when the record carries no error text,
// which no incident can be built from.
func ServerCause(r ServerErrorRecord) string {
	src := r.Attributes[LogKeyError]
	if src == "" {
		src = r.Attributes[LogKeyExceptionMessage]
	}
	if m := errSQLState.FindStringSubmatch(src); m != nil {
		return "SQLSTATE " + m[1]
	}
	if m := errRefused.FindStringSubmatch(src); m != nil {
		return "connection refused " + m[1]
	}
	if src != "" {
		return ErrorClass(src)
	}
	return ""
}
