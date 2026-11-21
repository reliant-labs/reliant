// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/anypb"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

func TestEveryRequestCarriesReliantClient(t *testing.T) {
	f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
		return &reliantv1.GetRunResponse{Run: &reliantv1.Run{Id: "r1"}}, nil
	}}
	c, _ := f.start(t)
	if _, err := c.Runs.Get(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	// A generated client built from HTTPClient, for an rpc the package does
	// not wrap, goes through the same transport.
	raw := reliantv1connect.NewRunServiceClient(c.HTTPClient(), c.BaseURL())
	if _, err := raw.GetRun(context.Background(), connect.NewRequest(&reliantv1.GetRunRequest{RunId: "r1"})); err != nil {
		t.Fatal(err)
	}
	if len(f.headers) != 2 {
		t.Fatalf("requests = %d, want 2", len(f.headers))
	}
	for i, h := range f.headers {
		if got, want := h.Get("Reliant-Client"), "sdk-go/"+clientVersion(); got != want {
			t.Errorf("request %d: Reliant-Client = %q, want %q", i, got, want)
		}
	}
	if strings.HasPrefix(clientVersion(), "v") || clientVersion() == "" {
		t.Errorf("version = %q: want semver without a leading v", clientVersion())
	}
	// The User-Agent is left as it was: connect-go sets its own on rpcs, so
	// Reliant-Client is the only identity an rpc carries.
}

func TestACallersOwnReliantClientIsKept(t *testing.T) {
	var got, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ua = r.Header.Get("Reliant-Client"), r.Header.Get("User-Agent")
	}))
	t.Cleanup(srv.Close)
	c, err := New(WithBaseURL(srv.URL), WithToken(testToken), WithInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
	req.Header.Set("Reliant-Client", "cli/1.9.2")
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got != "cli/1.9.2" {
		t.Errorf("Reliant-Client = %q, want the caller's cli/1.9.2", got)
	}
	if ua != "reliant-go-sdk/"+clientVersion() {
		t.Errorf("User-Agent = %q, want reliant-go-sdk/%s", ua, clientVersion())
	}
}

func TestVersionComesFromBuildInfo(t *testing.T) {
	dep := func(v string, replace *debug.Module) *debug.BuildInfo {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "example.com/app", Version: "(devel)"},
			Deps: []*debug.Module{
				{Path: "connectrpc.com/connect", Version: "v1.21.1"},
				{Path: modulePath, Version: v, Replace: replace},
			},
		}
	}
	cases := map[string]struct {
		bi   *debug.BuildInfo
		ok   bool
		want string
	}{
		"no build info":           {nil, false, Version},
		"a commit of main":        {dep("v0.0.0-20261120093000-abcdef123456", nil), true, "0.0.0-20261120093000-abcdef123456"},
		"a tag":                   {dep("v0.2.0", nil), true, "0.2.0"},
		"a module replace":        {dep("v0.1.0", &debug.Module{Path: "example.com/fork", Version: "v0.3.1"}), true, "0.3.1"},
		"a directory replace":     {dep("v0.0.0-00010101000000-000000000000", &debug.Module{Path: "../sdk/go"}), true, Version},
		"not a dependency at all": {&debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}}, true, Version},
		"the module itself, unstamped": {
			&debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "(devel)"}}, true, Version,
		},
		"the module itself, stamped from vcs": {
			&debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.0.0-20261120093000-abcdef123456+dirty"}}, true, "0.0.0-20261120093000-abcdef123456+dirty",
		},
	}
	for name, tc := range cases {
		if got := versionFromBuildInfo(tc.bi, tc.ok); got != tc.want {
			t.Errorf("%s: version = %q, want %q", name, got, tc.want)
		}
	}
}

// errorInfo builds a google.rpc.ErrorInfo detail from its wire form, as the
// server sends it.
func errorInfo(t *testing.T, reason, domain string, md map[string]string) *connect.ErrorDetail {
	t.Helper()
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, reason)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, domain)
	for k, v := range md {
		var e []byte
		e = protowire.AppendTag(e, 1, protowire.BytesType)
		e = protowire.AppendString(e, k)
		e = protowire.AppendTag(e, 2, protowire.BytesType)
		e = protowire.AppendString(e, v)
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, e)
	}
	d, err := connect.NewErrorDetail(&anypb.Any{TypeUrl: "type.googleapis.com/google.rpc.ErrorInfo", Value: b})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const upgradeMessage = "this sdk-go (0.1.0) is older than the minimum this server supports (0.2.0); upgrade: go get github.com/reliant-labs/reliant/sdk/go@latest"

var upgradeMetadata = map[string]string{
	"client_kind":     "sdk-go",
	"client_version":  "0.1.0",
	"min_version":     "0.2.0",
	"upgrade_url":     "https://reliantlabs.io/docs/sdk",
	"upgrade_command": "go get github.com/reliant-labs/reliant/sdk/go@latest",
}

func TestUpgradeRefusalIsErrUpgradeRequired(t *testing.T) {
	full := func(t *testing.T) *connect.Error {
		e := connect.NewError(connect.CodeFailedPrecondition, errors.New(upgradeMessage))
		e.Meta().Set(headerErrorReason, "upgrade_required")
		e.AddDetail(errorInfo(t, "UPGRADE_REQUIRED", "reliantlabs.io", upgradeMetadata))
		return e
	}
	headerOnly := func(*testing.T) *connect.Error {
		e := connect.NewError(connect.CodeFailedPrecondition, errors.New(upgradeMessage))
		e.Meta().Set(headerErrorReason, "upgrade_required")
		return e
	}
	detailOnly := func(t *testing.T) *connect.Error {
		e := connect.NewError(connect.CodeFailedPrecondition, errors.New(upgradeMessage))
		e.AddDetail(errorInfo(t, "UPGRADE_REQUIRED", "reliantlabs.io", upgradeMetadata))
		return e
	}
	cases := map[string]struct {
		refusal   func(*testing.T) *connect.Error
		wantFloor bool
	}{
		"header and detail": {full, true},
		"header only":       {headerOnly, false},
		"detail only":       {detailOnly, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
				return nil, tc.refusal(t)
			}}
			c, _ := f.start(t)
			_, err := c.Runs.Get(context.Background(), "r1")
			if !errors.Is(err, ErrUpgradeRequired) {
				t.Fatalf("errors.Is(%v, ErrUpgradeRequired) = false", err)
			}
			if !errors.Is(err, ErrFailedPrecondition) || connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Errorf("the refusal is still a FailedPrecondition: %v", err)
			}
			var e *Error
			if !errors.As(err, &e) || e.Reason != "upgrade_required" {
				t.Errorf("*Error = %+v, want Reason upgrade_required", e)
			}
			var u *UpgradeRequiredError
			if !errors.As(err, &u) {
				t.Fatalf("errors.As(%v, *UpgradeRequiredError) = false", err)
			}
			if u.Message != upgradeMessage {
				t.Errorf("Message = %q", u.Message)
			}
			want := UpgradeRequiredError{Message: upgradeMessage}
			if tc.wantFloor {
				want = UpgradeRequiredError{
					ClientKind: "sdk-go", ClientVersion: "0.1.0", MinVersion: "0.2.0",
					UpgradeURL: "https://reliantlabs.io/docs/sdk", UpgradeCommand: "go get github.com/reliant-labs/reliant/sdk/go@latest",
					Message: upgradeMessage,
				}
			}
			got := *u
			got.cause = nil
			if got != want {
				t.Errorf("UpgradeRequiredError = %+v, want %+v", got, want)
			}
			if !errors.Is(u, ErrUpgradeRequired) || connect.CodeOf(u) != connect.CodeFailedPrecondition {
				t.Errorf("the *UpgradeRequiredError alone: Is = %v, code = %v", errors.Is(u, ErrUpgradeRequired), connect.CodeOf(u))
			}
			if a, ok := AsUpgradeRequired(err); !ok || a != u {
				t.Errorf("AsUpgradeRequired = %v, %v; want the same error", a, ok)
			}
		})
	}
}

// The refusal of a client that sent no Reliant-Client names no floor, and is
// still an upgrade refusal.
func TestNoHeaderRefusalIsErrUpgradeRequired(t *testing.T) {
	const msg = "this client sends no Reliant-Client version; upgrade to the latest version"
	f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
		e := connect.NewError(connect.CodeFailedPrecondition, errors.New(msg))
		e.Meta().Set(headerErrorReason, "upgrade_required")
		e.AddDetail(errorInfo(t, "UPGRADE_REQUIRED", "reliantlabs.io", map[string]string{"client_kind": "", "min_version": ""}))
		return nil, e
	}}
	c, _ := f.start(t)
	_, err := c.Runs.Get(context.Background(), "r1")
	var u *UpgradeRequiredError
	if !errors.Is(err, ErrUpgradeRequired) || !errors.As(err, &u) {
		t.Fatalf("%v is not an upgrade refusal", err)
	}
	if u.MinVersion != "" || u.Message != msg || !strings.Contains(u.Error(), "upgrade to the latest version") {
		t.Errorf("UpgradeRequiredError = %+v (%q)", u, u.Error())
	}
}

func TestAsUpgradeRequiredReadsABareConnectError(t *testing.T) {
	e := connect.NewError(connect.CodeFailedPrecondition, errors.New(upgradeMessage))
	e.AddDetail(errorInfo(t, "UPGRADE_REQUIRED", "reliantlabs.io", upgradeMetadata))
	u, ok := AsUpgradeRequired(e)
	if !ok || u.MinVersion != "0.2.0" || u.UpgradeCommand != upgradeMetadata["upgrade_command"] {
		t.Fatalf("AsUpgradeRequired = %+v, %v", u, ok)
	}
	if !errors.Is(u, ErrUpgradeRequired) {
		t.Error("errors.Is(u, ErrUpgradeRequired) = false")
	}
}

func TestOtherRefusalsAreNotUpgradeRequired(t *testing.T) {
	plain := connect.NewError(connect.CodeFailedPrecondition, errors.New("run is not paused"))
	otherReason := connect.NewError(connect.CodeFailedPrecondition, errors.New("no"))
	otherReason.Meta().Set(headerErrorReason, "approval_not_pending")
	otherInfo := connect.NewError(connect.CodeFailedPrecondition, errors.New("no"))
	otherInfo.AddDetail(errorInfo(t, "QUOTA_EXCEEDED", "reliantlabs.io", nil))
	otherDomain := connect.NewError(connect.CodeFailedPrecondition, errors.New("no"))
	otherDomain.AddDetail(errorInfo(t, "UPGRADE_REQUIRED", "example.com", nil))
	for name, ce := range map[string]*connect.Error{
		"no reason": plain, "another reason": otherReason, "another ErrorInfo": otherInfo, "another domain": otherDomain,
	} {
		f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) { return nil, ce }}
		c, _ := f.start(t)
		_, err := c.Runs.Get(context.Background(), "r1")
		var u *UpgradeRequiredError
		if errors.Is(err, ErrUpgradeRequired) || errors.As(err, &u) {
			t.Errorf("%s: %v is an upgrade refusal", name, err)
		}
		if _, ok := AsUpgradeRequired(ce); ok {
			t.Errorf("%s: AsUpgradeRequired matched", name)
		}
		if !errors.Is(err, ErrFailedPrecondition) {
			t.Errorf("%s: not ErrFailedPrecondition", name)
		}
	}
}

func TestDecodeErrorInfoRejectsGarbage(t *testing.T) {
	if _, _, _, ok := decodeErrorInfo([]byte{0x0a, 0x05, 'a'}); ok {
		t.Error("a truncated field decoded")
	}
}
