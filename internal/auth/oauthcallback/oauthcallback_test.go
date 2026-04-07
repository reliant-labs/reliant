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

	result, err := Run(ctx, "https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}")
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

	cfg := CallbackConfig{
		ListenHost:   "127.0.0.1",
		RedirectHost: "localhost",
		CallbackPath: "/auth/callback",
		FixedPort:    1455,
	}
	server, _, err := newCallbackServer("https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}", cfg)
	if err != nil {
		t.Fatalf("newCallbackServer error: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:1455")
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
			resp, reqErr := http.Get("http://127.0.0.1:1455" + probePath)
			if reqErr == nil {
				resp.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = http.Get("http://127.0.0.1:1455/auth/callback?code=test-code&state=test-state")
	}()

	result, err := Run(context.Background(), "https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}")
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
	if result.RedirectURI != "http://localhost:1455/auth/callback" {
		t.Fatalf("RedirectURI = %q", result.RedirectURI)
	}
}

func TestRunFailsForIncompatibleExistingListener(t *testing.T) {
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
			"redirect_uri":  "http://localhost:1455/auth/callback",
			"active":        true,
		})
	})

	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	defer listener.Close()

	httpServer := &http.Server{Handler: mux}
	defer httpServer.Close()
	go func() { _ = httpServer.Serve(listener) }()

	_, err = Run(context.Background(), "https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to start callback listener") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTryReuseExistingListenerReadsResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(probePath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(probeResponse{
			Kind:         listenerKind,
			Version:      listenerVersion,
			CallbackPath: "/auth/callback",
			RedirectURI:  "http://localhost:1455/auth/callback",
			Active:       true,
		})
	})
	mux.HandleFunc(resultPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Result{
			Code:        "shared-code",
			State:       "shared-state",
			RedirectURI: "http://localhost:1455/auth/callback",
			CallbackURL: "/auth/callback?code=shared-code&state=shared-state",
		})
	})

	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	defer listener.Close()

	httpServer := &http.Server{Handler: mux}
	defer httpServer.Close()
	go func() { _ = httpServer.Serve(listener) }()

	result, err := tryReuseExistingListener(
		context.Background(),
		CallbackConfig{ListenHost: "127.0.0.1", CallbackPath: "/auth/callback", FixedPort: 1455},
		"http://localhost:1455/auth/callback",
		fmt.Errorf("listen tcp 127.0.0.1:1455: %w", syscallEADDRINUSE()),
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
