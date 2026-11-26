package lint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

func TestIdempotencyFindingsFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		rpc  idempotencyRPC
		want string // rule ID, "" for no finding
		msg  string // a substring of the message
	}{
		{name: "read without the option", rpc: idempotencyRPC{Name: "GetOrder"}, want: ruleIdempotencyReadUndeclared, msg: "named as a read (Get)"},
		{name: "read with the option", rpc: idempotencyRPC{Name: "GetOrder", NoSideEffects: true}},
		{name: "hook-generator read verb", rpc: idempotencyRPC{Name: "HasAccess"}, want: ruleIdempotencyReadUndeclared, msg: "(Has)"},
		{name: "activity read verb", rpc: idempotencyRPC{Name: "DescribeCluster"}, want: ruleIdempotencyReadUndeclared, msg: "(Describe)"},
		{name: "batch read", rpc: idempotencyRPC{Name: "BatchGetOrders"}, want: ruleIdempotencyReadUndeclared, msg: "(BatchGet)"},
		// Only BatchGet is a batch read, as in the hook generator and the
		// activity interceptor; no other Batch/Bulk prefix is stripped.
		{name: "BatchSearch is not judged", rpc: idempotencyRPC{Name: "BatchSearchOrders"}},
		{name: "BulkGet is not judged", rpc: idempotencyRPC{Name: "BulkGetOrders"}},
		{name: "BatchUpdate declaring NO_SIDE_EFFECTS is not judged", rpc: idempotencyRPC{Name: "BatchUpdateOrders", NoSideEffects: true}},
		{name: "server-streaming read", rpc: idempotencyRPC{Name: "WatchOrders"}, want: ruleIdempotencyReadUndeclared, msg: "(Watch)"},
		{name: "client-streaming read is not judged", rpc: idempotencyRPC{Name: "ListOrders", ClientStreaming: true}},
		{name: "whole-word match: Checkout is no read", rpc: idempotencyRPC{Name: "CheckoutCart"}},
		{name: "whole-word match: Issue is no read", rpc: idempotencyRPC{Name: "IssueRefund"}},
		{name: "noun name is not judged", rpc: idempotencyRPC{Name: "Health"}},
		{name: "noun name may declare it", rpc: idempotencyRPC{Name: "Version", NoSideEffects: true}},
		{name: "read-named write with a reason", rpc: idempotencyRPC{Name: "GetOrCreateSession", Mutation: true, MutationReason: "creates the session on first call"}},
		{name: "read-named write without a reason", rpc: idempotencyRPC{Name: "GetOrCreateSession", Mutation: true}, want: ruleIdempotencyReadUndeclared, msg: "with no reason"},
		{name: "bare forge:mutation on a write is not judged", rpc: idempotencyRPC{Name: "PlaceOrder", Mutation: true}},
		{name: "write declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "CreateOrder", NoSideEffects: true}, want: ruleIdempotencyWriteAsRead, msg: "named as a write (Create)"},
		{name: "revoke declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "RevokeKey", NoSideEffects: true}, want: ruleIdempotencyWriteAsRead, msg: "(Revoke)"},
		// Verbs a read-only rpc can open with are not judged.
		{name: "RunSavedQuery declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "RunSavedQuery", NoSideEffects: true}},
		{name: "ExecuteQuery declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "ExecuteQuery", NoSideEffects: true}},
		{name: "EnsureFresh declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "EnsureFresh", NoSideEffects: true}},
		{name: "StartPreview declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "StartPreview", NoSideEffects: true}},
		{name: "streaming write declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "SendMessages", ClientStreaming: true, NoSideEffects: true}, want: ruleIdempotencyWriteAsRead, msg: "(Send)"},
		{name: "write without the option", rpc: idempotencyRPC{Name: "CreateOrder"}},
		{name: "whole-word match: Settings is no write", rpc: idempotencyRPC{Name: "SettingsSummary", NoSideEffects: true}},
		{name: "forge:mutation and NO_SIDE_EFFECTS contradict", rpc: idempotencyRPC{Name: "FindAndHoldSeat", Mutation: true, MutationReason: "holds a seat", NoSideEffects: true}, want: ruleIdempotencyWriteAsRead, msg: "says both"},
		// A response carrying a secret or a per-call value: rule 3.
		{name: "secret read declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "GetUploadURL", NoSideEffects: true, SecretField: "upload_url", SecretWhy: "is named as a URL minted for this call"}, want: ruleIdempotencySecretResponse, msg: "carries `upload_url`"},
		{name: "secret read without the option replaces rule 1", rpc: idempotencyRPC{Name: "GetAccessToken", SecretField: "access_token", SecretWhy: "is named as a credential"}, want: ruleIdempotencySecretResponse, msg: "must not declare"},
		{name: "secret noun-named rpc declaring NO_SIDE_EFFECTS", rpc: idempotencyRPC{Name: "SessionChallenge", NoSideEffects: true, SecretField: "challenge", SecretWhy: "is named as a one-use value"}, want: ruleIdempotencySecretResponse, msg: "declares idempotency_level"},
		{name: "secret read marked forge:mutation with a reason", rpc: idempotencyRPC{Name: "GetUploadURL", Mutation: true, MutationReason: "issues a fresh URL", SecretField: "upload_url"}},
		{name: "secret read marked forge:mutation without a reason is rule 1's", rpc: idempotencyRPC{Name: "GetUploadURL", Mutation: true, SecretField: "upload_url"}, want: ruleIdempotencyReadUndeclared, msg: "with no reason"},
		{name: "secret write declaring NO_SIDE_EFFECTS is rule 2's alone", rpc: idempotencyRPC{Name: "CreateInvite", NoSideEffects: true, SecretField: "invite_url"}, want: ruleIdempotencyWriteAsRead, msg: "(Create)"},
		{name: "secret noun-named rpc without the option is not judged", rpc: idempotencyRPC{Name: "SessionChallenge", SecretField: "challenge"}},
		// Connect sends only a unary rpc as GET.
		{name: "secret server-streaming read is rule 1's", rpc: idempotencyRPC{Name: "WatchTokens", ServerStreaming: true, SecretField: "token"}, want: ruleIdempotencyReadUndeclared, msg: "(Watch)"},
		{name: "secret server-streaming read may declare it", rpc: idempotencyRPC{Name: "WatchTokens", ServerStreaming: true, NoSideEffects: true, SecretField: "token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.rpc.Service = "OrderService"
			got := idempotencyFindingsFor([]idempotencyRPC{tc.rpc})
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no finding, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want one %s finding, got %+v", tc.want, got)
			}
			if got[0].Rule != tc.want || got[0].Severity != finding.SeverityError {
				t.Errorf("rule/severity = %s/%s, want %s/error", got[0].Rule, got[0].Severity, tc.want)
			}
			if !strings.Contains(got[0].Message, tc.msg) || !strings.Contains(got[0].Message, "OrderService."+tc.rpc.Name) {
				t.Errorf("message %q lacks %q or the rpc name", got[0].Message, tc.msg)
			}
			if got[0].Remediation == "" {
				t.Error("finding has no fix hint")
			}
		})
	}
}

const idempotencyFixtureProto = `syntax = "proto3";

package shop.v1;

service ShopService {
  rpc GetOrder(GetOrderRequest) returns (GetOrderResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse);
  // forge:mutation: creates the cart on first call
  rpc GetOrCreateCart(GetOrderRequest) returns (GetOrderResponse);
  // forge:mutation
  rpc FindAndHoldSeat(GetOrderRequest) returns (GetOrderResponse);
  rpc CountOrders(GetOrderRequest) returns (GetOrderResponse); // forge:mutation — bumps a counter
  rpc CreateOrder(GetOrderRequest) returns (GetOrderResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  rpc RunSavedQuery(GetOrderRequest) returns (GetOrderResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  // forge:lint-disable-next-line forge-idempotency-write-marked-read: renders the mail it would send; sends nothing
  rpc SendTestPreview(GetOrderRequest) returns (GetOrderResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  rpc WatchOrders(GetOrderRequest) returns (stream GetOrderResponse);
  rpc SearchOrders(stream GetOrderRequest) returns (GetOrderResponse);
}
`

// writeIdempotencyProject writes proto/services/shop/v1/shop.proto and a
// descriptor carrying methods (what forge generate compiles from it).
func writeIdempotencyProject(t *testing.T, methods []codegen.Method) string {
	t.Helper()
	return writeIdempotencyProjectWith(t, idempotencyFixtureProto, methods, nil)
}

// writeIdempotencyProjectWith is writeIdempotencyProject over a given proto
// source and deep schema.
func writeIdempotencyProjectWith(t *testing.T, src string, methods []codegen.Method, schemas map[string][]codegen.SchemaFieldDef) string {
	t.Helper()
	root := t.TempDir()
	pdir := filepath.Join(root, "proto", "services", "shop", "v1")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "shop.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	desc := codegen.ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name: "ShopService", Package: "shop.v1", ProtoFile: "services/shop/v1/shop.proto", Methods: methods,
		Schemas: schemas,
	}}}
	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	ddir := filepath.Join(root, "gen", "forge_descriptor")
	if err := os.MkdirAll(ddir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ddir, "fixture.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCollectIdempotencyFindings(t *testing.T) {
	root := writeIdempotencyProject(t, []codegen.Method{
		{Name: "GetOrder", NoSideEffects: true},
		{Name: "ListOrders"},
		{Name: "GetOrCreateCart"},
		{Name: "FindAndHoldSeat"},
		{Name: "CountOrders"},
		{Name: "CreateOrder", NoSideEffects: true},
		{Name: "RunSavedQuery", NoSideEffects: true},
		{Name: "SendTestPreview", NoSideEffects: true},
		{Name: "WatchOrders", ServerStreaming: true},
		{Name: "SearchOrders", ClientStreaming: true},
	})
	fs, err := collectIdempotencyFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range fs {
		if f.File != "proto/services/shop/v1/shop.proto" {
			t.Errorf("finding file = %q, want the proto relative to the project root", f.File)
		}
		rpc := ""
		for _, name := range []string{"ListOrders", "FindAndHoldSeat", "CreateOrder", "WatchOrders", "GetOrCreateCart", "CountOrders", "RunSavedQuery", "SendTestPreview", "SearchOrders"} {
			if strings.Contains(f.Message, "ShopService."+name+" ") {
				rpc = name
			}
		}
		got[rpc] = f.Rule + "@" + itoa(f.Line)
	}
	want := map[string]string{
		"ListOrders":      ruleIdempotencyReadUndeclared + "@9",
		"FindAndHoldSeat": ruleIdempotencyReadUndeclared + "@13", // forge:mutation with no reason
		"CreateOrder":     ruleIdempotencyWriteAsRead + "@15",
		"WatchOrders":     ruleIdempotencyReadUndeclared + "@25",
	}
	if len(got) != len(want) {
		t.Errorf("got findings %v, want %v", got, want)
	}
	for rpc, w := range want {
		if got[rpc] != w {
			t.Errorf("%s: got %q, want %q", rpc, got[rpc], w)
		}
	}
}

// A project nothing has generated yet has no descriptor: the lane has
// nothing to judge and says nothing (every other lane already asks for a
// generate).
func TestCollectIdempotencyFindingsNoDescriptor(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := collectIdempotencyFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Errorf("want no findings with no descriptor and no protos, got %+v", fs)
	}
}

const idempotencySecretFixtureProto = `syntax = "proto3";

package shop.v1;

service ShopService {
  rpc GetUploadURL(GetUploadURLRequest) returns (GetUploadURLResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  rpc GetSession(GetSessionRequest) returns (GetSessionResponse);
  // forge:mutation: issues a fresh claim link on each call
  rpc GetClaimLink(GetClaimLinkRequest) returns (GetClaimLinkResponse);
  // forge:lint-disable-next-line forge-idempotency-secret-response-marked-read: a public, unsigned artifact URL
  rpc GetRelease(GetReleaseRequest) returns (GetReleaseResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
  // forge:lint-disable-next-line forge-idempotency-secret-response-marked-read: the token is a public key id
  rpc GetKeyID(GetKeyIDRequest) returns (GetKeyIDResponse);
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
}
`

// The rule reads each rpc's response through the descriptor's deep schema,
// takes rule 1's place on a read-named rpc, and honours forge:mutation and
// the lint-disable directive.
func TestCollectIdempotencyFindingsSecretResponse(t *testing.T) {
	root := writeIdempotencyProjectWith(t, idempotencySecretFixtureProto, []codegen.Method{
		{Name: "GetUploadURL", NoSideEffects: true, OutputTypeFQ: "shop.v1.GetUploadURLResponse"},
		{Name: "GetSession", OutputTypeFQ: "shop.v1.GetSessionResponse"},
		{Name: "GetClaimLink", OutputTypeFQ: "shop.v1.GetClaimLinkResponse"},
		{Name: "GetRelease", NoSideEffects: true, OutputTypeFQ: "shop.v1.GetReleaseResponse"},
		{Name: "GetKeyID", OutputTypeFQ: "shop.v1.GetKeyIDResponse"},
		{Name: "ListOrders", NoSideEffects: true, OutputTypeFQ: "shop.v1.ListOrdersResponse"},
	}, map[string][]codegen.SchemaFieldDef{
		"shop.v1.GetUploadURLResponse": {{Name: "upload_url", Kind: "string"}},
		"shop.v1.GetSessionResponse":   {{Name: "session", Kind: "message", TypeName: "shop.v1.Session"}},
		"shop.v1.Session":              {{Name: "user_id", Kind: "string"}, {Name: "access_token", Kind: "string"}},
		"shop.v1.GetClaimLinkResponse": {{Name: "claim_url", Kind: "string"}},
		"shop.v1.GetReleaseResponse":   {{Name: "download_url", Kind: "string"}},
		"shop.v1.GetKeyIDResponse":     {{Name: "token", Kind: "string"}},
		"shop.v1.ListOrdersResponse":   {{Name: "next_page_token", Kind: "string"}},
	})
	fs, err := collectIdempotencyFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Rule+"@"+itoa(f.Line))
		if f.Rule == ruleIdempotencySecretResponse && f.Line == 9 && !strings.Contains(f.Message, "`session.access_token`") {
			t.Errorf("GetSession finding %q does not name the nested field", f.Message)
		}
	}
	want := []string{
		ruleIdempotencySecretResponse + "@6", // GetUploadURL declares the option
		ruleIdempotencySecretResponse + "@9", // GetSession: rule 3 in rule 1's place
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got findings %v, want %v", got, want)
	}
}
