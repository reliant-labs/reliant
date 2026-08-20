// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"errors"
	"testing"
)

// TestLoginNonInteractiveNeverOpensBrowser is the reproduction for the prod
// bug: the daemon must never open a browser or start the local login-page
// HTTP server when it has no credentials and cannot run an interactive login.
// Before the fix, Login had no way to refuse the interactive flow at all —
// this test fails against that code (it opens a browser and hangs waiting for
// a callback that never comes) and passes once LoginOptions.NonInteractive
// short-circuits before any of that setup runs.
func TestLoginNonInteractiveNeverOpensBrowser(t *testing.T) {
	withCompiledAuthDefaults(t, "http://auth.localhost", "test-key")

	browserOpened := false
	original := openBrowser
	openBrowser = func(string) error {
		browserOpened = true
		return nil
	}
	defer func() { openBrowser = original }()

	_, err := Login(context.Background(), LoginOptions{NonInteractive: true})
	if err == nil {
		t.Fatal("Login(NonInteractive) returned nil error, want ErrNonInteractiveLoginRequired")
	}
	if !errors.Is(err, ErrNonInteractiveLoginRequired) {
		t.Fatalf("Login(NonInteractive) error = %v, want ErrNonInteractiveLoginRequired", err)
	}
	if browserOpened {
		t.Fatal("Login(NonInteractive) opened a browser — it must never do so")
	}
}

// TestLoginInteractiveStillOpensBrowser guards the other direction: the
// existing interactive behavior (used by `reliant daemon register` and `auth
// login`) must be unchanged by the new option defaulting to false.
func TestLoginInteractiveStillOpensBrowser(t *testing.T) {
	withCompiledAuthDefaults(t, "http://auth.localhost", "test-key")

	browserOpened := make(chan string, 1)
	original := openBrowser
	openBrowser = func(url string) error {
		browserOpened <- url
		return nil
	}
	defer func() { openBrowser = original }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Login(ctx, LoginOptions{})
	}()

	select {
	case <-browserOpened:
		// Expected: Login opened the browser. Cancel to unblock the
		// goroutine waiting on the callback that will never arrive in a test.
		cancel()
	case <-done:
		t.Fatal("Login returned before opening the browser")
	}
	<-done
}
