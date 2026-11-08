// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// The real thing, as every DB-backed test reaches it.
func TestCheckServesADatabaseFromTheSharedServer(t *testing.T) {
	t.Cleanup(pgtest.Shutdown)
	if err := check(context.Background(), 150*time.Second, pgtest.New); err != nil {
		t.Fatal(failure(err))
	}
}

func TestCheckReportsAServerThatCannotServeADatabase(t *testing.T) {
	refused := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	err := check(context.Background(), time.Second, func() (*sql.DB, func(), error) { return nil, nil, refused })
	if !errors.Is(err, refused) {
		t.Fatalf("check must return pgtest's error, got %v", err)
	}
	msg := failure(err)
	for _, want := range []string{"FAILED", "shared", "connection refused", "not this change"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure message must say %q:\n%s", want, msg)
		}
	}
}

// The never-answering case end to end: a boot that never returns holds
// pgtest's lock, so Shutdown never returns either. run must still exit, and
// fail, within the timeout plus the shutdown grace.
func TestRunExitsWhenTheBootAndShutdownBothHang(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	hang := func() (*sql.DB, func(), error) {
		<-block
		return nil, nil, errors.New("unreachable")
	}
	var stdout, stderr strings.Builder
	rc := make(chan int, 1)
	go func() {
		rc <- run(context.Background(), 50*time.Millisecond, 50*time.Millisecond, hang, func() { <-block }, &stdout, &stderr)
	}()
	select {
	case got := <-rc:
		if got != 1 {
			t.Fatalf("exit status %d, want 1\n%s", got, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run waited on a Shutdown that never returns: the gate's pgtest phase would hang")
	}
	for _, want := range []string{"did not shut down within", "did not answer in time"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr must say %q:\n%s", want, stderr.String())
		}
	}
}

// A check that fails outright still shuts pgtest down before exiting 1.
func TestRunShutsPgtestDownAfterAFailedCheck(t *testing.T) {
	shut := false
	var stdout, stderr strings.Builder
	got := run(context.Background(), time.Second, time.Second,
		func() (*sql.DB, func(), error) { return nil, nil, errors.New("connection refused") },
		func() { shut = true }, &stdout, &stderr)
	if got != 1 || !shut {
		t.Fatalf("exit %d (want 1), shutdown called %v (want true)\n%s", got, shut, stderr.String())
	}
	if strings.Contains(stderr.String(), "did not shut down") {
		t.Errorf("a Shutdown that returned was reported as hung:\n%s", stderr.String())
	}
}

func TestCheckGivesUpOnAServerThatNeverAnswers(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	err := check(context.Background(), 50*time.Millisecond, func() (*sql.DB, func(), error) {
		<-block
		return nil, nil, errors.New("unreachable")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("check must time out, got %v", err)
	}
	if msg := failure(err); !strings.Contains(msg, "did not answer in time") {
		t.Errorf("a timeout must say so:\n%s", msg)
	}
}
