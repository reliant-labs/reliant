// Copyright (c) 2025 Reliant Labs
package observability

import (
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func collectFleet(t *testing.T) string {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(daemonFleetCollector{})
	mfs, err := reg.Gather()
	require.NoError(t, err)
	var b strings.Builder
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			fmt.Fprintf(&b, "%s/%s=%v\n", labels["version"], labels["commit"], m.GetGauge().GetValue())
		}
	}
	return b.String()
}

func TestDaemonFleetGaugeIsLabelledByVersionAndCommit(t *testing.T) {
	t.Cleanup(func() { SetDaemonFleetSource(nil) })
	SetDaemonFleetSource(func() []DaemonFleetBuild {
		return []DaemonFleetBuild{
			{Version: "1.8.3-dev+b675d224", Commit: "b675d22417c3deadbeef", Count: 3},
			{Version: "", Commit: "", Count: 2},
		}
	})
	got := collectFleet(t)
	require.Contains(t, got, "1.8.3-dev+b675d224/b675d22417c3=3\n", "the commit is cut to a fixed prefix")
	require.Contains(t, got, "unknown/unknown=2\n", "a daemon that reported no build is 'unknown'")
	require.Equal(t, 2, testutil.CollectAndCount(daemonFleetCollector{}))
}

func TestDaemonFleetGaugeEmitsNothingWithoutASource(t *testing.T) {
	SetDaemonFleetSource(nil)
	require.Empty(t, collectFleet(t))
}

// Cardinality is bounded however many distinct builds daemons report: the
// smallest fold into one "other" series and the total stays right.
func TestDaemonFleetGaugeCardinalityIsBounded(t *testing.T) {
	t.Cleanup(func() { SetDaemonFleetSource(nil) })
	var builds []DaemonFleetBuild
	total := 0
	for i := 0; i < maxFleetSeries*3; i++ {
		builds = append(builds, DaemonFleetBuild{Version: fmt.Sprintf("v%d", i), Commit: fmt.Sprintf("%040d", i), Count: 1 + i%5})
		total += 1 + i%5
	}
	SetDaemonFleetSource(func() []DaemonFleetBuild { return builds })

	require.Equal(t, maxFleetSeries, testutil.CollectAndCount(daemonFleetCollector{}))
	sum := 0
	for _, line := range strings.Split(strings.TrimSpace(collectFleet(t)), "\n") {
		var n int
		_, err := fmt.Sscanf(line[strings.LastIndex(line, "=")+1:], "%d", &n)
		require.NoError(t, err)
		sum += n
	}
	require.Equal(t, total, sum)
	require.Contains(t, collectFleet(t), "other/other=")
}

func TestDaemonFleetGaugeSurvivesAnInvalidUTF8Label(t *testing.T) {
	t.Cleanup(func() { SetDaemonFleetSource(nil) })
	// 17 two-byte runes cut at 48 bytes would split a rune.
	SetDaemonFleetSource(func() []DaemonFleetBuild {
		return []DaemonFleetBuild{{Version: strings.Repeat("é", 30), Commit: "\xff\xfe", Count: 1}}
	})
	require.NotPanics(t, func() { _ = collectFleet(t) })
}
