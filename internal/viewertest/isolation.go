// Copyright (c) 2025 Reliant Labs

package viewertest

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// Who is the caller of an isolation case. Owner, Viewer and Admin are members
// of the fixture's org (the viewer and admin see the owner's shared chat);
// OtherTenant is a member of another org only.
type Who string

const (
	AsOwner       Who = "owner"
	AsViewer      Who = "viewer"
	AsAdmin       Who = "admin"
	AsOtherTenant Who = "other tenant"
)

// IsolationCase is one rpc asked as one caller, and the codes it may answer.
// It holds codes and scoping only, never message text or response bytes, so a
// rewrite of a handler's error style or wire shape does not touch it.
type IsolationCase struct {
	// Name labels the subtest: "GetPlan/other tenant".
	Name string
	// Rpc is the handler method, such as "GetPlan".
	Rpc string
	// As is the caller.
	As Who
	// Request builds the request; it may seed rows of its own, so a case that
	// deletes or updates never changes what a later case reads.
	Request func(t *testing.T, f *Fixture) proto.Message
	// Want lists the codes that are right; OK is a success. More than one
	// entry is a transition: the code answered today and the one the
	// conversion should answer.
	Want []connect.Code
	// TODO names the wave that tightens Want, shown when the case fails.
	TODO string
	// After runs once the call has returned, with its error (nil on success),
	// to check what the call left behind: a refused write changed no row.
	After func(t *testing.T, f *Fixture, err error)
}

// OK is the code of a call that succeeded, for IsolationCase.Want.
const OK connect.Code = 0

// Code is the expectation "exactly this code".
func Code(c connect.Code) []connect.Code { return []connect.Code{c} }

// RefusedToday is the expectation for a caller outside the row's access:
// the code answered today, and NotFound, which a converted rpc answers (the
// owner's decision to stop telling a caller that another tenant's row
// exists). The same test holds before and after the conversion; tighten it to
// NotFound alone in the conversion PR. OK is never accepted.
func RefusedToday(today connect.Code) []connect.Code {
	if today == connect.CodeNotFound {
		return Code(today)
	}
	return []connect.Code{today, connect.CodeNotFound}
}

// RunIsolation runs every case against svc (a service's handler, as for
// RunService), each as a subtest.
func RunIsolation(t *testing.T, f *Fixture, svc any, cases []IsolationCase) {
	t.Helper()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			if len(tc.Want) == 0 {
				t.Fatalf("viewertest: case %q lists no accepted code", tc.Name)
			}
			ctx := f.Owner
			switch tc.As {
			case AsOwner:
			case AsViewer:
				ctx = f.Viewer
			case AsAdmin:
				ctx = f.Admin
			case AsOtherTenant:
				ctx = f.Outsider
			default:
				t.Fatalf("viewertest: case %q has no caller", tc.Name)
			}
			a := invoke(t, svc, tc.Rpc, ctx, tc.Request(t, f))
			if a.panic != nil {
				t.Fatalf("%s as %s panicked: %v", tc.Rpc, tc.As, a.panic)
			}
			got := OK
			if a.err != nil {
				got = a.code
			}
			accepted := false
			for _, w := range tc.Want {
				accepted = accepted || got == w
			}
			if !accepted {
				todo := ""
				if tc.TODO != "" {
					todo = fmt.Sprintf(" (TODO %s)", tc.TODO)
				}
				t.Errorf("%s as %s answered %v, want one of %v%s", tc.Rpc, tc.As, a, tc.Want, todo)
			}
			if tc.After != nil {
				tc.After(t, f, a.err)
			}
		})
	}
}
