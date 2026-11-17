package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSignalVerdict(t *testing.T) {
	tests := []struct {
		name string
		rows [][]string
		want Status
		has  string
	}{
		{"nothing recent is a warning, not a pass", nil, StatusWarn, "no traces"},
		{"zero counts are nothing", [][]string{{"api", "0"}}, StatusWarn, "no traces"},
		{"junk rows are ignored", [][]string{{"api"}, {"api", "x"}}, StatusWarn, "no traces"},
		{"rows name the senders, busiest first", [][]string{{"worker", "2"}, {"api", "9"}}, StatusPass, "11 spans from 2 service(s): api, worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := signalVerdict("traces", "spans", tt.rows)
			if got.Status != tt.want || !strings.Contains(got.Message, tt.has) {
				t.Fatalf("got %s %q, want %s containing %q", got.Status, got.Message, tt.want, tt.has)
			}
		})
	}
}

func TestMetricsQueryReadsEveryFamily(t *testing.T) {
	var sql string
	env := &Environment{Compose: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		sql = args[len(args)-1]
		return []byte("api\t3\n"), nil
	}}
	if got := CheckMetrics(context.Background(), env); got.Status != StatusPass {
		t.Fatalf("metrics = %s %q", got.Status, got.Message)
	}
	for _, table := range metricTables {
		if !strings.Contains(sql, "default."+table) {
			t.Errorf("metrics check never reads %s; an app exporting only that family reads as having no metrics", table)
		}
	}
}

func TestSignalCheckSeparatesDownFromUnaskable(t *testing.T) {
	down := &Environment{Compose: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New(`service "clickstack" is not running`)
	}}
	if got := CheckTraces(context.Background(), down); got.Status != StatusSkip {
		t.Errorf("clickstack not running = %s, want skip (observability off is not a fault)", got.Status)
	}
	broken := &Environment{Compose: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("Authentication failed")
	}}
	if got := CheckTraces(context.Background(), broken); got.Status != StatusUnknown {
		t.Errorf("a query that failed for another reason = %s, want unknown", got.Status)
	}
}
