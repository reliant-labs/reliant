package obsbackend

import "testing"

func TestSpanKeyAttributes(t *testing.T) {
	for _, tc := range []struct {
		name string
		span Span
		want string
	}{
		{"a canceled rpc, its error.type the same code", Span{Name: "a.v1.S/Get", Attributes: map[string]string{
			"rpc.method": "a.v1.S/Get", "rpc.response.status_code": "CANCELED", "error.type": "CANCELED"}}, "rpc=CANCELED"},
		{"an ok rpc says nothing", Span{Name: "a.v1.S/Get", Attributes: map[string]string{"rpc.grpc.status_code": "0"}}, ""},
		{"http", Span{Name: "POST x", Attributes: map[string]string{"http.response.status_code": "403", "error.type": "403"}}, "http=403"},
		{"a ClickHouse read", Span{Name: "SELECT t.otel_logs", Attributes: map[string]string{
			"db.query.summary": "SELECT t.otel_logs", "db.clickhouse.read_rows": "14012832", "db.clickhouse.read_bytes": "3221225472",
			"db.response.returned_rows": "12", "db.clickhouse.query_id": "q-1", "error.type": "TIMEOUT_EXCEEDED"}},
			"error.type=TIMEOUT_EXCEEDED read_rows=14.0M read_bytes=3.0GiB rows=12 query_id=q-1"},
		{"a bare operation shows its statement", Span{Name: "SELECT", Attributes: map[string]string{"db.query.text": "SELECT a FROM (SELECT ...)"}},
			`db="SELECT a FROM (SELECT ...)"`},
		{"a named statement does not repeat it", Span{Name: "SELECT users", Attributes: map[string]string{"db.query.text": "SELECT * FROM users"}}, ""},
	} {
		if got := FormatKVs(SpanKeyAttributes(tc.span)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLogReasonAndAttributes(t *testing.T) {
	attrs := map[string]string{"error": "canceled: the request was canceled", "cause": "context canceled", "code": "canceled",
		"log.file.path": "/var/log/pods/x/0.log", "logtag": "F", "procedure": "/a.v1.S/Get"}
	if got := FormatKVs(LogReason(attrs)); got != `error="canceled: the request was canceled" cause="context canceled" code=canceled` {
		t.Errorf("reason = %s", got)
	}
	kept := LogAttributes(attrs)
	if _, ok := kept["log.file.path"]; ok || kept["procedure"] != "/a.v1.S/Get" || len(kept) != 4 {
		t.Errorf("attributes = %v", kept)
	}
	if LogAttributes(map[string]string{"logtag": "F"}) != nil {
		t.Error("nothing left is nil")
	}
}

func TestSpanMessage(t *testing.T) {
	s := Span{StatusMessage: "status says", Events: []SpanEvent{{Name: "exception", Attributes: map[string]string{"exception.type": "*errors.x", "exception.message": "boom"}}}}
	if got := SpanMessage(s); got != "*errors.x: boom" {
		t.Errorf("%q", got)
	}
	s.Events = nil
	if got := SpanMessage(s); got != "status says" {
		t.Errorf("%q", got)
	}
}
