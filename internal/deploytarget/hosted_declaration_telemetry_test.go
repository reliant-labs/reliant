package deploytarget

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// rawCapsCaller answers GetCapabilities with a raw proto3-JSON body.
type rawCapsCaller string

func (c rawCapsCaller) Call(_ context.Context, _ string, _, out any) error {
	return json.Unmarshal([]byte(c), out)
}

// GetCapabilities.telemetry_limits reaches the reader: a number or a 64-bit
// integer's decimal string, an unset or non-positive cap left to the default,
// and a server that advertises none leaves the reader on the defaults.
func TestHostedBundleReaderReadsTelemetryLimits(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want *release.TelemetryLimits
	}{
		"none":    {`{"bundleDocumentFeatures":["shape-telemetry"]}`, nil},
		"numbers": {`{"telemetryLimits":{"maxSampleLimit":2000,"maxPulledTargets":5}}`, &release.TelemetryLimits{MaxSampleLimit: 2000, MaxPulledTargets: 5}},
		"int64":   {`{"telemetryLimits":{"maxSampleLimit":"2000"}}`, &release.TelemetryLimits{MaxSampleLimit: 2000}},
		"zero":    {`{"telemetryLimits":{"maxPulledTargets":0}}`, &release.TelemetryLimits{}},
	} {
		r, err := HostedBundleReader(context.Background(), rawCapsCaller(c.body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if (r.Telemetry == nil) != (c.want == nil) || (c.want != nil && *r.Telemetry != *c.want) {
			t.Errorf("%s: Telemetry = %+v; want %+v", name, r.Telemetry, c.want)
		}
	}
	r, _ := HostedBundleReader(context.Background(), rawCapsCaller(`{"telemetryLimits":{"maxPulledTargets":5}}`))
	if got := r.TelemetryCaps(); got != (release.TelemetryLimits{MaxSampleLimit: release.DefaultHostedTelemetryMaxSampleLimit, MaxPulledTargets: 5}) {
		t.Errorf("TelemetryCaps = %+v", got)
	}
}
