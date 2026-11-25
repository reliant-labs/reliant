package codegen

import "testing"

func TestScanRPCCommentMarkers(t *testing.T) {
	const src = `syntax = "proto3";
service S {
  // forge:mutation: holds a seat
  rpc FindAndHoldSeat(Req) returns (Resp);
  // Reads the seat map.
  // forge:mutation — claims a hold
  //   (and records it)
  rpc FindAndClaim(Req) returns (Resp);
  // forge:mutation
  rpc FindBare(Req) returns (Resp);
  rpc CountAndBump(Req) returns (Resp); // forge:mutation - bumps a counter
  /* forge:mutation: block comment */
  rpc GetOrCreate(Req) returns (Resp);
  // forge:mutation: detached by a blank line

  rpc FindSeat(Req) returns (Resp);
  // forge:mutations is a different token
  rpc FindOther(Req) returns (Resp);
  // forge:query-scope Seat
  rpc ListSeats(Req) returns (Resp);
}
`
	m := ScanRPCCommentMarkers(src)
	for name, want := range map[string]struct {
		mutation bool
		reason   string
		line     int
	}{
		"FindAndHoldSeat": {true, "holds a seat", 4},
		"FindAndClaim":    {true, "claims a hold", 8},
		"FindBare":        {true, "", 10},
		"CountAndBump":    {true, "bumps a counter", 11},
		"GetOrCreate":     {true, "block comment", 13},
		"FindSeat":        {false, "", 16},
		"FindOther":       {false, "", 18},
		"ListSeats":       {false, "", 20},
	} {
		if got := m.Mutation[name]; got != want.mutation {
			t.Errorf("%s: Mutation = %v, want %v", name, got, want.mutation)
		}
		if got := m.MutationReason[name]; got != want.reason {
			t.Errorf("%s: MutationReason = %q, want %q", name, got, want.reason)
		}
		if got := m.Lines[name]; got != want.line {
			t.Errorf("%s: line = %d, want %d", name, got, want.line)
		}
	}
	if got := m.QueryScope["ListSeats"]; got != "Seat" {
		t.Errorf("ListSeats query scope = %q, want Seat", got)
	}
	if _, ok := m.QueryScope["FindSeat"]; ok {
		t.Error("a query scope leaked onto an unmarked rpc")
	}
}
