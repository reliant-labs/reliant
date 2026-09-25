package oauthcallback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRunCancelsWhenContextDone(t *testing.T) {
	originalOpenBrowser := openBrowser
	openBrowser = func(string) error { return nil }
	defer func() { openBrowser = originalOpenBrowser }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	result, err := RunWithConfig(ctx, codexAuthorizeTemplate, codexShapedConfig(t))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if result != nil {
		t.Fatalf("expected nil result, got %#v", result)
	}
	if !strings.Contains(err.Error(), "OAuth callback cancelled") {
		t.Fatalf("expected cancellation message, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunReusesCompatibleExistingListener(t *testing.T) {
	originalOpenBrowser := openBrowser
	openBrowser = func(string) error { return nil }
	defer func() { openBrowser = originalOpenBrowser }()

	cfg := codexShapedConfig(t)
	server, _, err := newCallbackServer(codexAuthorizeTemplate, cfg)
	if err != nil {
		t.Fatalf("newCallbackServer error: %v", err)
	}

	listener, err := net.Listen("tcp", listenAddr(cfg))
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	defer listener.Close()

	httpServer := &http.Server{Handler: server.handler()}
	defer httpServer.Close()
	go func() { _ = httpServer.Serve(listener) }()

	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		deadline := time.Now().Add(2 * time.Second)
		for {
			resp, reqErr := http.Get(baseURL(cfg) + probePath)
			if reqErr == nil {
				resp.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = http.Get(baseURL(cfg) + "/auth/callback?code=test-code&state=test-state")
	}()

	result, err := RunWithConfig(context.Background(), codexAuthorizeTemplate, cfg)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	<-callbackDone

	if result.Code != "test-code" {
		t.Fatalf("Code = %q, want %q", result.Code, "test-code")
	}
	if result.State != "test-state" {
		t.Fatalf("State = %q, want %q", result.State, "test-state")
	}
	if want := fmt.Sprintf("http://localhost:%d/auth/callback", cfg.FixedPort); result.RedirectURI != want {
		t.Fatalf("RedirectURI = %q, want %q", result.RedirectURI, want)
	}
}

func TestRunFailsForIncompatibleExistingListener(t *testing.T) {
	cfg := codexShapedConfig(t)
	originalOpenBrowser := openBrowser
	openBrowser = func(string) error { return nil }
	defer func() { openBrowser = originalOpenBrowser }()

	mux := http.NewServeMux()
	mux.HandleFunc(probePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind":          "someone-else",
			"version":       1,
			"callback_path": "/auth/callback",
			"redirect_uri":  fmt.Sprintf("http://localhost:%d/auth/callback", cfg.FixedPort),
			"active":        true,
		})
	})

	listener, err := net.Listen("tcp", listenAddr(cfg))
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	defer listener.Close()

	httpServer := &http.Server{Handler: mux}
	defer httpServer.Close()
	go func() { _ = httpServer.Serve(listener) }()

	_, err = RunWithConfig(context.Background(), codexAuthorizeTemplate, cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to start callback listener") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTryReuseExistingListenerReadsResult(t *testing.T) {
	cfg := codexShapedConfig(t)
	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", cfg.FixedPort)
	mux := http.NewServeMux()
	mux.HandleFunc(probePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(probeResponse{
			Kind:         listenerKind,
			Version:      listenerVersion,
			CallbackPath: "/auth/callback",
			RedirectURI:  redirectURI,
			Active:       true,
		})
	})
	mux.HandleFunc(resultPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Result{
			Code:        "shared-code",
			State:       "shared-state",
			RedirectURI: redirectURI,
			CallbackURL: "/auth/callback?code=shared-code&state=shared-state",
		})
	})

	listener, err := net.Listen("tcp", listenAddr(cfg))
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	defer listener.Close()

	httpServer := &http.Server{Handler: mux}
	defer httpServer.Close()
	go func() { _ = httpServer.Serve(listener) }()

	result, err := tryReuseExistingListener(
		context.Background(),
		cfg,
		redirectURI,
		fmt.Errorf("listen tcp %s: %w", listenAddr(cfg), syscallEADDRINUSE()),
	)
	if err != nil {
		t.Fatalf("tryReuseExistingListener error: %v", err)
	}
	if result.Code != "shared-code" || result.State != "shared-state" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func syscallEADDRINUSE() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	busyListener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err == nil {
		busyListener.Close()
		ln.Close()
		return fmt.Errorf("expected address in use")
	}
	ln.Close()
	return err
}
