// Copyright (c) 2025 Reliant Labs

package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

const canary = "CANARY-sk-live-9f8e7d6c"

func TestSecretNeverFormatsPlaintext(t *testing.T) {
	s := NewSecret([]byte(canary))
	type wrapper struct {
		Token Secret
		Ptr   *Secret
	}
	w := wrapper{Token: s, Ptr: &s}

	outputs := map[string]string{
		"%v": fmt.Sprintf("%v", s), "%+v": fmt.Sprintf("%+v", s), "%#v": fmt.Sprintf("%#v", s),
		"%s": fmt.Sprintf("%s", s), "%q": fmt.Sprintf("%q", s), "%x": fmt.Sprintf("%x", s),
		"%d": fmt.Sprintf("%d", s), "Sprint": fmt.Sprint(s), "Sprintln": fmt.Sprintln(s),
		"struct %+v": fmt.Sprintf("%+v", w), "struct %#v": fmt.Sprintf("%#v", w),
		"errorf": fmt.Errorf("call failed: %v", s).Error(),
		"join":   errors.Join(errors.New("a"), fmt.Errorf("%w", errors.New(fmt.Sprint(s)))).Error(),
		"String": s.String(), "GoString": s.GoString(),
	}
	for name, out := range outputs {
		if strings.Contains(out, canary) {
			t.Errorf("%s leaked plaintext: %q", name, out)
		}
	}
	if got := fmt.Sprintf("%v", s); got != Redacted {
		t.Errorf("%%v = %q, want %q", got, Redacted)
	}

	for name, v := range map[string]any{"value": s, "pointer": &s, "struct": w, "map": map[string]Secret{"k": s}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal(%s): %v", name, err)
		}
		if bytes.Contains(b, []byte(canary)) {
			t.Errorf("json %s leaked plaintext: %s", name, b)
		}
	}
}

func TestSecretNeverLogsPlaintext(t *testing.T) {
	var buf bytes.Buffer
	for _, h := range []slog.Handler{slog.NewJSONHandler(&buf, nil), slog.NewTextHandler(&buf, nil)} {
		l := slog.New(h)
		s := NewSecret([]byte(canary))
		l.Info("with attr", "token", s)
		l.Info("with group", slog.Group("g", slog.Any("token", s)))
		l.With("token", s).Info("with logger")
		l.Info(fmt.Sprintf("interpolated %v", s))
	}
	if strings.Contains(buf.String(), canary) {
		t.Fatalf("log output leaked plaintext:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), Redacted) {
		t.Fatalf("expected %q in log output:\n%s", Redacted, buf.String())
	}
}

func TestSecretUseExposesCopyAndZeroes(t *testing.T) {
	src := []byte(canary)
	s := NewSecret(src)
	for i := range src {
		src[i] = 'x'
	}
	var kept []byte
	if err := s.Use(func(b []byte) error {
		if string(b) != canary {
			t.Errorf("Use saw %q", b)
		}
		kept = b
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range kept {
		if c != 0 {
			t.Fatalf("Use did not zero its copy: %q", kept)
		}
	}
	if err := s.Use(func(b []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if s.Len() != len(canary) {
		t.Errorf("Len = %d", s.Len())
	}
}

// Reflection pin (design §3.2): the only exported surface of Secret that can
// yield its bytes is Use. Adding a getter must be a conscious act that edits
// this allow-list.
func TestSecretExposesNoAccessorsBeyondUse(t *testing.T) {
	allowed := map[string]bool{
		"Use": true, "Len": true, "String": true, "GoString": true, "Format": true,
		"MarshalJSON": true, "MarshalText": true, "LogValue": true,
	}
	typ := reflect.TypeOf(Secret{})
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; !allowed[name] {
			t.Errorf("Secret grew exported method %q; vault.Secret may only expose %v", name, allowed)
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); f.IsExported() {
			t.Errorf("Secret has exported field %q", f.Name)
		}
	}
}
