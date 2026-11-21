// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
)

// ErrUpgradeRequired: the server refused the call because this SDK is older
// than the minimum it supports. Upgrade the module; retrying does not help.
// errors.As with an *UpgradeRequiredError gives the floor and how to upgrade.
var ErrUpgradeRequired = errors.New("reliant: upgrade required")

// UpgradeRequiredError is the server's refusal of a client older than its
// floor. Every field but Message may be empty: a server that sends only the
// reason header names no floor.
type UpgradeRequiredError struct {
	// ClientKind is the client the server judged, e.g. "sdk-go".
	ClientKind string
	// ClientVersion is the version the server saw in Reliant-Client.
	ClientVersion string
	// MinVersion is the oldest version the server accepts.
	MinVersion string
	// UpgradeURL is where to read how to upgrade.
	UpgradeURL string
	// UpgradeCommand is a command that upgrades the client.
	UpgradeCommand string
	// Message is the server's explanation.
	Message string

	cause error
}

func (e *UpgradeRequiredError) Error() string {
	if e.Message != "" {
		return "reliant: upgrade required: " + e.Message
	}
	msg := "reliant: upgrade required"
	if e.MinVersion != "" {
		msg += fmt.Sprintf(": the server needs %s %s or newer", e.ClientKind, e.MinVersion)
	}
	if how := e.how(); how != "" {
		msg += "; upgrade: " + how
	}
	return msg
}

func (e *UpgradeRequiredError) how() string {
	if e.UpgradeCommand != "" {
		return e.UpgradeCommand
	}
	return e.UpgradeURL
}

// Is makes errors.Is(err, ErrUpgradeRequired) work.
func (e *UpgradeRequiredError) Is(target error) bool { return target == ErrUpgradeRequired }

// Unwrap exposes the underlying Connect error, so connect.CodeOf works through
// it.
func (e *UpgradeRequiredError) Unwrap() error { return e.cause }

// AsUpgradeRequired reports the upgrade refusal in err's chain. It works on
// any error a call returned: this package's *Error, and the bare
// *connect.Error a generated client built from [Client.HTTPClient] returns.
func AsUpgradeRequired(err error) (*UpgradeRequiredError, bool) {
	var u *UpgradeRequiredError
	if errors.As(err, &u) {
		return u, true
	}
	ce, ok := asConnectError(err)
	if !ok {
		return nil, false
	}
	u = upgradeRequired(ce)
	return u, u != nil
}

// The refusal's wire shape (the server's errreason.UpgradeRequired, and the
// google.rpc.ErrorInfo it attaches).
const (
	reasonUpgradeRequired = "upgrade_required"
	errorInfoType         = "google.rpc.ErrorInfo"
	errorInfoReason       = "UPGRADE_REQUIRED"
	errorInfoDomain       = "reliantlabs.io"
)

// upgradeRequired decodes an upgrade refusal from ce: the reason header, or
// the ErrorInfo detail (which also names the floor). Nil when ce is not one.
func upgradeRequired(ce *connect.Error) *UpgradeRequiredError {
	byHeader := ce.Meta().Get(headerErrorReason) == reasonUpgradeRequired
	var md map[string]string
	byDetail := false
	for _, d := range ce.Details() {
		if d.Type() != errorInfoType {
			continue
		}
		reason, domain, m, ok := decodeErrorInfo(d.Bytes())
		if ok && reason == errorInfoReason && (domain == errorInfoDomain || domain == "") {
			byDetail, md = true, m
			break
		}
	}
	if !byHeader && !byDetail {
		return nil
	}
	return &UpgradeRequiredError{
		ClientKind:     md["client_kind"],
		ClientVersion:  md["client_version"],
		MinVersion:     md["min_version"],
		UpgradeURL:     md["upgrade_url"],
		UpgradeCommand: md["upgrade_command"],
		Message:        ce.Message(),
		cause:          ce,
	}
}

// decodeErrorInfo reads a google.rpc.ErrorInfo from its wire bytes: reason
// (1), domain (2) and metadata (3, map<string, string>). It is decoded by hand
// so the module needs no genproto dependency for one three-field message.
func decodeErrorInfo(b []byte) (reason, domain string, md map[string]string, ok bool) {
	md = map[string]string{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", "", nil, false
		}
		b = b[n:]
		if typ != protowire.BytesType {
			if n = protowire.ConsumeFieldValue(num, typ, b); n < 0 {
				return "", "", nil, false
			}
			b = b[n:]
			continue
		}
		v, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return "", "", nil, false
		}
		b = b[n:]
		switch num {
		case 1:
			reason = string(v)
		case 2:
			domain = string(v)
		case 3:
			k, val, ok := decodeMapEntry(v)
			if !ok {
				return "", "", nil, false
			}
			md[k] = val
		}
	}
	return reason, domain, md, true
}

func decodeMapEntry(b []byte) (key, value string, ok bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", "", false
		}
		b = b[n:]
		if typ != protowire.BytesType {
			if n = protowire.ConsumeFieldValue(num, typ, b); n < 0 {
				return "", "", false
			}
			b = b[n:]
			continue
		}
		v, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return "", "", false
		}
		b = b[n:]
		switch num {
		case 1:
			key = string(v)
		case 2:
			value = string(v)
		}
	}
	return key, value, true
}

// Client identity: every request carries Reliant-Client: sdk-go/<version>,
// which the server compares with its floor for this SDK.
const (
	headerClient = "Reliant-Client"
	clientKind   = "sdk-go"
	modulePath   = "github.com/reliant-labs/reliant/sdk/go"
)

// clientVersion is the version of this module the running binary was built
// with, without the leading v: what the consumer's go.mod resolved (a
// pseudo-version such as 0.0.0-20261120093000-abcdef123456 for a commit of
// main), so it is right without anyone stamping it at release. A build that
// records none (a directory replace, a binary built without module support)
// falls back to [Version].
var clientVersion = sync.OnceValue(func() string {
	bi, ok := debug.ReadBuildInfo()
	return versionFromBuildInfo(bi, ok)
})

func versionFromBuildInfo(bi *debug.BuildInfo, ok bool) string {
	if !ok || bi == nil {
		return Version
	}
	if bi.Main.Path == modulePath {
		return moduleVersion(bi.Main.Version)
	}
	for _, m := range bi.Deps {
		if m == nil || m.Path != modulePath {
			continue
		}
		// A replace names the code that was built; the require line's version
		// does not. A directory replace has no version at all.
		if m.Replace != nil {
			m = m.Replace
		}
		return moduleVersion(m.Version)
	}
	return Version
}

func moduleVersion(v string) string {
	if v == "" || v == "(devel)" {
		return Version
	}
	return strings.TrimPrefix(v, "v")
}

// clientHeader is the Reliant-Client value this SDK sends.
func clientHeader() string { return clientKind + "/" + clientVersion() }
