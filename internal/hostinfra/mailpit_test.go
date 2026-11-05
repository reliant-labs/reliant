package hostinfra

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/smtp"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMailpitArgs(t *testing.T) {
	spec := Spec{Name: "mail", Engine: EngineMailpit, Port: 1025}
	args := mailpitArgs(spec, "/d")
	for _, want := range []string{"127.0.0.1:1025", "127.0.0.1:8025", filepath.Join("/d", "mailpit.db"), "--disable-version-check"} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v lack %q", args, want)
		}
	}
	if MailpitUIURL(spec) != "http://localhost:8025" {
		t.Errorf("ui url = %s", MailpitUIURL(spec))
	}
}

func TestMailpitRefusesAPortWithNoRoomForItsUI(t *testing.T) {
	if err := validateMailpit(Spec{Name: "mail", Port: 60000}); err == nil || !strings.Contains(err.Error(), "at most 58535") {
		t.Errorf("got %v", err)
	}
	if err := validateMailpit(Spec{Name: "mail", Port: 1025}); err != nil {
		t.Error(err)
	}
}

func TestMailpitBanner(t *testing.T) {
	for line, want := range map[string]bool{
		"220 host Mailpit ESMTP Service ready\r\n": true,
		"220 host ESMTP Postfix\r\n":               false,
		"421 Mailpit busy\r\n":                     false,
	} {
		if got := mailpitBanner(line); got != want {
			t.Errorf("mailpitBanner(%q) = %v, want %v", line, got, want)
		}
	}
}

// mailpitPort finds a free SMTP port whose UI port (port+7000) is free too.
func mailpitPort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if p > 65535-mailpitUIPortOffset {
			p -= 20000 // ephemeral ports run high; try below them
		}
		if !portListening(p) && !portListening(p+mailpitUIPortOffset) {
			return p
		}
	}
	t.Skip("no free port pair for mailpit")
	return 0
}

// The real thing: fetch the pinned Mailpit, start it, deliver a message over
// SMTP exactly as the dev IdP does (plain, no auth), read it back through the
// API the developer's UI uses, then restart idempotently and stop.
func TestMailpitStartsCatchesMailAndStops(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads and runs Mailpit; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("exercised on unix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	project := t.TempDir()
	spec := Spec{Name: "mail", Engine: EngineMailpit, Port: mailpitPort(t)}
	if err := Start(ctx, project, spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _, _ = StopReport(project, spec) })

	addr := "127.0.0.1:" + strconv.Itoa(spec.Port)
	msg := "From: no-reply@acme.test\r\nTo: owner@acme.test\r\nSubject: You are invited\r\n\r\nhttp://localhost:3000/auth/invite?code=ABC\r\n"
	if err := smtp.SendMail(addr, nil, "no-reply@acme.test", []string{"owner@acme.test"}, []byte(msg)); err != nil {
		t.Fatalf("send: %v", err)
	}
	var list struct {
		Messages []struct {
			Subject string `json:"Subject"`
		} `json:"messages"`
	}
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(MailpitUIPort(spec)) + "/api/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 || list.Messages[0].Subject != "You are invited" {
		t.Fatalf("messages = %+v", list.Messages)
	}

	st, err := StatusOf(ctx, project, spec)
	if err != nil || !st.Running || !st.Ready {
		t.Fatalf("status: %+v %v", st, err)
	}
	if err := Start(ctx, project, spec); err != nil {
		t.Fatalf("second start was not idempotent: %v", err)
	}
	if stopped, err := StopReport(project, spec); err != nil || !stopped {
		t.Fatalf("stop: %v %v", stopped, err)
	}
	if portListening(spec.Port) {
		t.Fatal("SMTP port still held after stop")
	}
}
