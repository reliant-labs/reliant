package obsdash

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// TestRendererMapsEveryFieldTheValidatorAccepts pins the boundary between the
// published validator and this renderer: a field dashspec.CheckField accepts
// on a signal is one fieldSQL can draw, and one it refuses is refused here
// with the same error. A field added to one side only fails this test.
func TestRendererMapsEveryFieldTheValidatorAccepts(t *testing.T) {
	fields := []string{
		FieldService, FieldEnv, "resource.attr.k8s.pod.name", "timestamp",
		"span.name", "span.kind", "span.status", "span.status_message", "span.duration", "span.attr.http.route",
		"log.severity", "log.body", "log.attr.user", "metric.attr.route",
		"metric.name", "span.attr", "log.level", "duration", "",
	}
	for _, sig := range []Signal{Traces, Logs, Metrics} {
		for _, f := range fields {
			checkErr := dashspec.CheckField(sig, f)
			sql, sqlErr := fieldSQL(sig, f)
			switch {
			case checkErr == nil && (sqlErr != nil || sql == ""):
				t.Errorf("%s %q: validator accepts it, renderer has no column (%v)", sig, f, sqlErr)
			case checkErr != nil && sqlErr == nil:
				t.Errorf("%s %q: validator refuses it (%v), renderer maps it to %s", sig, f, checkErr, sql)
			case checkErr != nil && sqlErr.Error() != checkErr.Error():
				t.Errorf("%s %q: errors differ: %v vs %v", sig, f, sqlErr, checkErr)
			}
		}
	}
}
