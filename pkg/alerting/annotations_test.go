// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"strings"
	"testing"
)

func TestAnnotationTemplateRendering(t *testing.T) {
	labels := map[string]string{"service": "api", "signal": "traces", "errors": "12.0004", "total": "3400", "uri": "/oauth/v2/token"}
	for _, tc := range []struct {
		name  string
		text  string
		value float64
		want  string
	}{
		// The forms the substitution supported render exactly as before.
		{"plain text", "x", 1, "x"},
		{"label", "{{ $labels.service }} is slow", 1, "api is slow"},
		{"value six digits", "p95 is {{$value}}ms", 812.123456789, "p95 is 812.123ms"},
		{"value large", "{{ $value }}", 1234567, "1.23457e+06"},
		{"missing label is empty", "[{{ $labels.nope }}]", 1, "[]"},
		{"tight braces", "{{$labels.service}}/{{$value}}", 2, "api/2"},
		// Prometheus conventions.
		{"dot forms", "{{ .Labels.service }} {{ .Value }}", 0.5, "api 0.5"},
		{"printf value", `{{ printf "%.0f" $value }} failed`, 41.6, "42 failed"},
		{"printf precision", `{{ printf "%.2f" $value }}`, 0.98765, "0.99"},
		{"printf label number", `({{ printf "%.0f" $labels.errors }} of {{ printf "%.0f" $labels.total }})`, 1, "(12 of 3400)"},
		{"printf d of float", `{{ printf "%d" $value }}`, 6.7, "7"},
		{"printf non-numeric label left alone", `{{ printf "%s" $labels.uri }}`, 1, "/oauth/v2/token"},
		{"printf percent literal", `{{ printf "%.1f%%" $value }}`, 3.14159, "3.1%"},
		{"mulf", `refusing {{ printf "%.0f" (mulf $value 100.0) }}% of {{ $labels.signal }}`, 0.257, "refusing 26% of traces"},
		{"divf addf subf", `{{ divf (addf $value 2) 4 }} {{ subf $value 1 }}`, 6, "2 5"},
		{"humanize", "{{ humanize $value }}", 1234567, "1.235M"},
		{"humanize small", "{{ humanize $value }}", 0.0012, "1.2m"},
		{"humanize1024", "{{ humanize1024 $value }}", 1048576, "1Mi"},
		{"humanizeDuration", "{{ humanizeDuration $value }}", 90061, "1d 1h 1m 1s"},
		{"humanizeDuration seconds", "{{ humanizeDuration $value }}", 1.5, "1.5s"},
		{"humanizeDuration ms", "{{ humanizeDuration $value }}", 0.25, "250ms"},
		{"humanizePercentage", "{{ humanizePercentage $value }}", 0.1234, "12.34%"},
		{"toUpper", "{{ toUpper $labels.service }}", 1, "API"},
		{"if gt", `{{ if gt .Value 10.0 }}high{{ else }}low{{ end }}`, 11, "high"},
		{"with", `{{ with $labels.service }}svc={{ . }}{{ end }}`, 1, "svc=api"},
		{"trim markers", "a {{- $labels.service -}} b", 1, "aapib"},
		// Anything that cannot render is sent as written.
		{"unknown function", "{{ query \"up\" }} {{ $labels.service }}", 1, "{{ query \"up\" }} {{ $labels.service }}"},
		{"unknown variable", "{{ $externalURL }}", 1, "{{ $externalURL }}"},
		{"unclosed action", "{{ $labels.service", 1, "{{ $labels.service"},
		{"exec error", `{{ mulf $labels.uri 2 }}`, 1, `{{ mulf $labels.uri 2 }}`},
		{"range refused", "{{ range 1000000000 }}x{{ end }}", 1, "{{ range 1000000000 }}x{{ end }}"},
		{"template refused", `{{ define "a" }}x{{ end }}{{ template "a" }}`, 1, `{{ define "a" }}x{{ end }}{{ template "a" }}`},
		{"huge width refused", `{{ printf "%0999999999d" 1 }}`, 1, `{{ printf "%0999999999d" 1 }}`},
		{"dynamic width refused at exec", `{{ printf (print "%0" "99999" "d") 1 }}`, 1, `{{ printf (print "%0" "99999" "d") 1 }}`},
		{"star width refused", `{{ printf "%*d" 99999 1 }}`, 1, `{{ printf "%*d" 99999 1 }}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRule()
			r.Annotations = map[string]string{"summary": tc.text}
			ss, err := buildSeries(r, []Row{{"service": labels["service"], "signal": labels["signal"], "errors": labels["errors"], "total": labels["total"], "uri": labels["uri"], "value": tc.value}})
			if err != nil {
				t.Fatal(err)
			}
			if got := ss[0].annotations["summary"]; got != tc.want {
				t.Fatalf("rendered %q, want %q", got, tc.want)
			}
			if fellBack := tc.want == tc.text && strings.Contains(tc.text, "{{"); fellBack != (ss[0].annotationErr != nil) {
				t.Fatalf("annotationErr = %v, fell back = %v", ss[0].annotationErr, fellBack)
			}
		})
	}
}

func TestAnnotationOutputIsBounded(t *testing.T) {
	r := testRule()
	r.Annotations = map[string]string{"summary": strings.Repeat("{{ $labels.big }}", 50)}
	ss, err := buildSeries(r, []Row{{"big": strings.Repeat("é", 600), "value": 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	got := ss[0].annotations["summary"]
	if ss[0].annotationErr != nil {
		t.Fatalf("hitting the limit is not a failure: %v", ss[0].annotationErr)
	}
	if len(got) > maxLabelValueBytes || len(got) < maxLabelValueBytes-1 || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("output not cut cleanly at the limit: %d bytes", len(got))
	}
}

func TestValidateAnnotations(t *testing.T) {
	for _, ok := range []map[string]string{
		nil,
		{"summary": "plain", "runbook_url": "https://wiki/x"},
		{"summary": `{{ $labels.service }} fails {{ printf "%.1f" (mulf $value 100.0) }}% ({{ humanize $value }})`},
	} {
		if err := ValidateAnnotations(ok); err != nil {
			t.Errorf("ValidateAnnotations(%v) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		"{{ $labels.service",
		"{{ query \"up\" }}",
		"{{ range 3 }}x{{ end }}",
		`{{ block "b" . }}x{{ end }}`,
		`{{ printf "%.999f" $value }}`,
		`{{ printf "%[2]d %[1]d" 1 2 }}`,
	} {
		err := ValidateAnnotations(map[string]string{"summary": bad})
		if err == nil || !strings.Contains(err.Error(), `annotation "summary"`) {
			t.Errorf("ValidateAnnotations(%q) = %v, want an error naming the annotation", bad, err)
		}
	}
}

// TestControlPlaneSummariesRender pins the forms control-plane's alert KCL
// uses (deploy/kcl/lib/selfmon_alerts.k): every function there must be one
// this package provides.
func TestControlPlaneSummariesRender(t *testing.T) {
	for _, tc := range []struct {
		text, want string
		value      float64
	}{
		{`Machine {{ $labels.daemon_id }} unhealthy for {{ printf "%.0f" $value }}s`, "Machine d-1 unhealthy for 301s", 300.6},
		{`The telemetry gateway is refusing {{ printf "%.0f" (mulf $value 100.0) }}% of {{ $labels.signal }}`, "The telemetry gateway is refusing 13% of logs", 0.126},
		{`{{ printf "%.1f" $value }}% of {{ $labels.uri }} requests answered 5xx in 10m ({{ printf "%.0f" $labels.errors }} of {{ printf "%.0f" $labels.total }})`, "2.5% of /token requests answered 5xx in 10m (5 of 200)", 2.5},
		{`latency {{ printf "%.2f" $value }}s`, "latency 1.23s", 1.234},
	} {
		if err := ValidateAnnotations(map[string]string{"summary": tc.text}); err != nil {
			t.Fatalf("%q: %v", tc.text, err)
		}
		got, err := compileAnnotations(map[string]string{"summary": tc.text}).expand(
			map[string]string{"daemon_id": "d-1", "signal": "logs", "uri": "/token", "errors": "5", "total": "200"}, tc.value)
		if err != nil || got["summary"] != tc.want {
			t.Fatalf("%q rendered %q (%v), want %q", tc.text, got["summary"], err, tc.want)
		}
	}
}
