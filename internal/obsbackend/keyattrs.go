package obsbackend

import (
	"strconv"
	"strings"
)

// KV is one attribute picked for a one-line summary.
type KV struct{ Key, Value string }

// maxKeyValue bounds a value shown inline: a statement or URL can be any
// length, and the tree is one line per span.
const maxKeyValue = 120

// SpanKeyAttributes are the attributes that say what a span did and how it
// ended, in the order a reader wants them: the RPC or HTTP outcome, the
// error class, then what a database statement was and how much it read.
// Attributes that only repeat the span's name are left out. Empty when the
// span carries none of them.
func SpanKeyAttributes(s Span) []KV {
	a := s.Attributes
	if len(a) == 0 {
		return nil
	}
	var out []KV
	seen := map[string]bool{}
	add := func(k, v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, KV{k, clip(v)})
	}
	// The RPC's outcome, under whichever convention the instrumentation used.
	for _, k := range []string{"rpc.response.status_code", "rpc.connect_rpc.error_code", "rpc.grpc.status_code"} {
		if v := a[k]; v != "" && v != "0" && !strings.EqualFold(v, "OK") {
			add("rpc", v)
			break
		}
	}
	if m := a["rpc.method"]; m != "" && !strings.Contains(s.Name, m) {
		add("rpc.method", m)
	}
	for _, k := range []string{"http.response.status_code", "http.status_code"} {
		if v := a[k]; v != "" {
			add("http", v)
			break
		}
	}
	// error.type often repeats the code just shown (403, CANCELED).
	if v := a["error.type"]; v != "" {
		dup := false
		for _, kv := range out {
			dup = dup || strings.EqualFold(kv.Value, v)
		}
		if !dup {
			add("error.type", v)
		}
	}
	if sum := a["db.query.summary"]; sum != "" && sum != s.Name {
		add("db", sum)
	} else if text := firstNonEmpty(a["db.query.text"], a["db.statement"]); text != "" && !strings.Contains(s.Name, " ") {
		// The name is a bare operation ("SELECT"): the statement is the
		// only thing that says which.
		add("db", text)
	}
	if v := a["db.clickhouse.read_rows"]; v != "" && v != "0" {
		add("read_rows", humanCount(v))
	}
	if v := a["db.clickhouse.read_bytes"]; v != "" && v != "0" {
		add("read_bytes", humanBytes(v))
	}
	if v := a["db.response.returned_rows"]; v != "" {
		add("rows", v)
	}
	if v := a["db.clickhouse.query_id"]; v != "" {
		add("query_id", v)
	}
	return out
}

// SpanMessage is why a failed span failed: its exception ("type: message"),
// else its status message. "" when it says nothing.
func SpanMessage(s Span) string {
	for _, e := range s.Events {
		if e.Name != "exception" {
			continue
		}
		t, m := e.Attributes["exception.type"], e.Attributes["exception.message"]
		switch {
		case t != "" && m != "":
			return t + ": " + m
		case m != "":
			return m
		case t != "":
			return t
		}
	}
	return s.StatusMessage
}

// LogReason is what a log record says about a failure beyond its body: the
// error, its cause, the exception and the code, the attributes forge's
// logging interceptor and svcerr write. A body like "rpc failed" says that
// something failed; these say what.
func LogReason(attrs map[string]string) []KV {
	var out []KV
	for _, k := range []string{"error", "cause", "exception.type", "exception.message", "code", "error_class"} {
		if v := strings.TrimSpace(attrs[k]); v != "" {
			out = append(out, KV{k, clip(v)})
		}
	}
	return out
}

// logNoise are attributes a log shipper or the record itself adds that say
// nothing a reader of one trace needs: file positions, stream tags and
// copies of the record's own time, level and ids.
var logNoise = map[string]bool{
	"log.file.name": true, "log.file.path": true, "log.iostream": true, "logtag": true,
	"time": true, "level": true, "msg": true, "trace_id": true, "span_id": true,
}

// LogAttributes are a record's attributes without the shipper's noise; nil
// when nothing is left.
func LogAttributes(attrs map[string]string) map[string]string {
	var out map[string]string
	for k, v := range attrs {
		if logNoise[k] {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// FormatKVs renders kvs as `k=v` pairs, quoting values with spaces.
func FormatKVs(kvs []KV) string {
	parts := make([]string, len(kvs))
	for i, kv := range kvs {
		v := kv.Value
		if strings.ContainsAny(v, " \t\"=") {
			v = strconv.Quote(v)
		}
		parts[i] = kv.Key + "=" + v
	}
	return strings.Join(parts, " ")
}

func clip(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if len(v) <= maxKeyValue {
		return v
	}
	return v[:maxKeyValue-1] + "…"
}

func humanCount(v string) string {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return v
	}
	switch {
	case n >= 1e9:
		return strconv.FormatFloat(n/1e9, 'f', 1, 64) + "G"
	case n >= 1e6:
		return strconv.FormatFloat(n/1e6, 'f', 1, 64) + "M"
	case n >= 1e4:
		return strconv.FormatFloat(n/1e3, 'f', 0, 64) + "k"
	}
	return v
}

func humanBytes(v string) string {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return v
	}
	for _, u := range []struct {
		div  float64
		unit string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if n >= u.div {
			return strconv.FormatFloat(n/u.div, 'f', 1, 64) + u.unit
		}
	}
	return v + "B"
}
