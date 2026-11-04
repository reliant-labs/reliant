// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: OpenTelemetry/Prometheus provider setup and label-safety helpers; process-global by design
package observability

import (
	"sort"
	"strings"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// DaemonFleetBuild is how many daemons one gateway replica holds a stream for,
// per reported build. An unknown build is the zero Build.
type DaemonFleetBuild struct {
	Version string
	Commit  string
	Count   int
}

const (
	// unknownBuildLabel labels a daemon that reported no build (it predates the
	// report, or the binary was not stamped).
	unknownBuildLabel = "unknown"
	// otherBuildLabel collects the builds past maxFleetSeries.
	otherBuildLabel = "other"
	// maxFleetSeries bounds reliant_daemon_fleet's cardinality. The labels come
	// from the daemon's own registration and are trusted for nothing, so a
	// client reporting a fresh build per connect must not mint a series each.
	maxFleetSeries = 64
	// maxBuildLabelLen caps a label value for the same reason.
	maxBuildLabelLen = 48
	// commitLabelLen is the sha prefix kept: enough to name a build, and the
	// same length however the daemon spelled it.
	commitLabelLen = 12
)

var daemonFleetDesc = prometheus.NewDesc(
	"reliant_daemon_fleet",
	"Daemons attached to this gateway replica, by the build they reported (version and commit; 'unknown' when a daemon reported none). Sum across replicas for the fleet.",
	[]string{"version", "commit"}, nil,
)

var daemonFleetSource atomic.Pointer[func() []DaemonFleetBuild]

// SetDaemonFleetSource sets where reliant_daemon_fleet reads the attached
// daemons from at scrape time, replacing any earlier source. Reading at scrape
// keeps the gauge exact across supersedes, teardowns and reconnects, which an
// inc/dec kept alongside the connection map would not. Passing nil clears it.
func SetDaemonFleetSource(source func() []DaemonFleetBuild) {
	if source == nil {
		daemonFleetSource.Store(nil)
		return
	}
	daemonFleetSource.Store(&source)
}

type daemonFleetCollector struct{}

func (daemonFleetCollector) Describe(ch chan<- *prometheus.Desc) { ch <- daemonFleetDesc }

func (daemonFleetCollector) Collect(ch chan<- prometheus.Metric) {
	source := daemonFleetSource.Load()
	if source == nil {
		return
	}
	for _, s := range boundFleetSeries((*source)()) {
		ch <- prometheus.MustNewConstMetric(daemonFleetDesc, prometheus.GaugeValue, float64(s.Count), s.Version, s.Commit)
	}
}

// boundFleetSeries normalises the builds' labels, merges the builds that now
// share one, and keeps the largest maxFleetSeries, folding the rest into one
// "other" series so the total stays right.
func boundFleetSeries(builds []DaemonFleetBuild) []DaemonFleetBuild {
	type key struct{ version, commit string }
	merged := make(map[key]int, len(builds))
	for _, b := range builds {
		if b.Count <= 0 {
			continue
		}
		merged[key{fleetLabel(b.Version, maxBuildLabelLen), fleetLabel(b.Commit, commitLabelLen)}] += b.Count
	}
	out := make([]DaemonFleetBuild, 0, len(merged))
	for k, n := range merged {
		out = append(out, DaemonFleetBuild{Version: k.version, Commit: k.commit, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Commit < out[j].Commit
	})
	if len(out) <= maxFleetSeries {
		return out
	}
	rest := 0
	for _, b := range out[maxFleetSeries-1:] {
		rest += b.Count
	}
	out = append(out[:maxFleetSeries-1], DaemonFleetBuild{Version: otherBuildLabel, Commit: otherBuildLabel, Count: rest})
	return out
}

func fleetLabel(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	// Prometheus panics on an invalid UTF-8 label value, and the cut above can
	// split a multibyte rune of one the daemon sent.
	s = strings.ToValidUTF8(s, "")
	if s == "" {
		return unknownBuildLabel
	}
	return s
}
