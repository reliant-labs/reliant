// Copyright (c) 2025 Reliant Labs

package shell

import (
	"testing"
	"time"
)

// countScans replaces the OS scan refreshPortsGated runs with a counter.
func countScans(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := scanPorts
	scanPorts = func([]*BackgroundProcess) { n++ }
	t.Cleanup(func() { scanPorts = prev })
	return &n
}

// A listing within portScanFreshness of a scan of every process reuses it,
// and the next one after that window scans again.
func TestRefreshPortsGated_ReusesAFreshScanOfEveryProcess(t *testing.T) {
	scans := countScans(t)
	now := time.Unix(1_000, 0)
	m := newTestBGManager()
	m.clock = func() time.Time { return now }

	m.refreshPortsGated(nil, true)
	m.refreshPortsGated(nil, true)
	now = now.Add(portScanFreshness / 2)
	m.refreshPortsGated(nil, false)
	if *scans != 1 {
		t.Fatalf("scans within the freshness window = %d, want 1", *scans)
	}

	now = now.Add(portScanFreshness)
	m.refreshPortsGated(nil, true)
	if *scans != 2 {
		t.Fatalf("scans after the window = %d, want 2", *scans)
	}
}

// A subset's scan says nothing about the other processes, so it never makes
// the next full listing skip its scan.
func TestRefreshPortsGated_ASubsetScanIsNotFreshForEveryone(t *testing.T) {
	scans := countScans(t)
	m := newTestBGManager()

	m.refreshPortsGated(nil, false)
	m.refreshPortsGated(nil, false)
	m.refreshPortsGated(nil, true)
	if *scans != 3 {
		t.Fatalf("scans = %d, want 3 (two subsets, then every process)", *scans)
	}
}

// A listing never waits behind a scan already running: it returns at once
// with the ports the processes already hold.
func TestRefreshPortsGated_NeverWaitsOnARunningScan(t *testing.T) {
	scans := countScans(t)
	m := newTestBGManager()

	m.portScan.mu.Lock() // a scan in flight
	done := make(chan struct{})
	go func() {
		m.refreshPortsGated(nil, true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refreshPortsGated waited on the running scan")
	}
	m.portScan.mu.Unlock()
	if *scans != 0 {
		t.Fatalf("scans = %d, want 0: the running scan answers for the caller", *scans)
	}
}
