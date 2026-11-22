package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/reliant-labs/forge/pkg/authn"
)

// TestTraceHandlerNamesTheUser pins NS FG-10: a record logged inside an
// authenticated request carries user.id, the key an issue's affected-users
// count reads; a record that names a user itself keeps its own.
func TestTraceHandlerNamesTheUser(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil)))
	ctx := authn.ContextWithUserID(context.Background(), "u-42")
	log.ErrorContext(ctx, "boom")
	log.InfoContext(ctx, "acting for another", slog.String("user.id", "u-7"))
	log.InfoContext(context.Background(), "anonymous")

	var lines []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if lines[0]["user.id"] != "u-42" || lines[1]["user.id"] != "u-7" {
		t.Fatalf("user.id = %v / %v, want u-42 / u-7", lines[0]["user.id"], lines[1]["user.id"])
	}
	if _, ok := lines[2]["user.id"]; ok {
		t.Fatalf("a record with no principal named a user: %v", lines[2])
	}
}

// With OTLP logs on, the bridge's record names the user too: the enrichment
// sits above the stdout/OTLP split, not only in the stdout branch.
func TestOTLPBridgeNamesTheUser(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	prev := otel.GetLoggerProvider()
	t.Cleanup(func() { otel.SetLoggerProvider(prev) })
	cap := &captureProcessor{}
	otel.SetLoggerProvider(sdklog.NewLoggerProvider(sdklog.WithProcessor(cap)))

	var buf bytes.Buffer
	log := slog.New(NewLogHandler(slog.NewJSONHandler(&buf, nil), Config{ServiceName: "svc"}))
	log.ErrorContext(authn.ContextWithUserID(context.Background(), "u-42"), "boom")
	if len(cap.recs) != 1 || cap.recs[0][authn.AttrUserID].AsString() != "u-42" {
		t.Fatalf("bridge records = %v, want one naming user.id u-42", cap.recs)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"user.id":"u-42"`)) {
		t.Errorf("stdout line lacks user.id: %s", buf.String())
	}
}

// An id that is not opaque never reaches a record, a span or a product event.
func TestNonOpaqueUserIDNeverReachesTelemetry(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJl"
	for _, id := range []string{"ada@example.com", "10.0.0.7", "[::1]", jwt, strings.Repeat("a", authn.MaxUserIDLen+1)} {
		var buf bytes.Buffer
		log := slog.New(NewLogHandler(slog.NewJSONHandler(&buf, nil), Config{}))
		ctx := authn.ContextWithUserID(context.Background(), id)
		if got := authn.UserIDFromContext(ctx); got != "" {
			t.Errorf("%q was recorded as the user (%q)", id, got)
		}
		log.ErrorContext(ctx, "boom")
		if bytes.Contains(buf.Bytes(), []byte("user.id")) {
			t.Errorf("%q reached a record: %s", id, buf.String())
		}
	}
	if !authn.OpaqueUserID("usr_01HZX") || authn.OpaqueUserID("a@b") {
		t.Error("OpaqueUserID disagrees with the actor rule")
	}
}
