package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeDescriptorProject lays down the two files the service parsers read —
// go.mod and an UNSTAMPED gen/forge_descriptor.json (no proto tree to hash,
// so the staleness guard does not apply) — and returns the project dir.
func writeDescriptorProject(t *testing.T, services ...ServiceDef) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ForgeDescriptor{Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func serviceNames(defs []ServiceDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

// ParseServicesFromProtos is the funnel every service-scoped consumer reads
// (handler stubs, mocks, CRUD, composition, cmd subcommands, frontend hooks,
// audit, scaffold). An unmanaged service leaking through it is exactly the
// big-bang the marker exists to prevent, so the filter is pinned HERE rather
// than at each consumer — and the full set stays reachable by name.
func TestParseServices_UnmanagedLeftOutOfTheManagedSet(t *testing.T) {
	dir := writeDescriptorProject(t,
		ServiceDef{Name: "ChatService", Package: "app.v1", Methods: []Method{{Name: "SendMessage"}}, Unmanaged: true},
		ServiceDef{Name: "BillingService", Package: "app.v1", Methods: []Method{{Name: "GetInvoice"}}},
	)

	managed, err := ParseServicesFromProtos("", dir)
	if err != nil {
		t.Fatalf("ParseServicesFromProtos: %v", err)
	}
	if got := serviceNames(managed); len(got) != 1 || got[0] != "BillingService" {
		t.Fatalf("managed services = %v, want [BillingService]", got)
	}
	if managed[0].ModulePath != "example.com/app" {
		t.Errorf("managed service ModulePath = %q, want it set from go.mod", managed[0].ModulePath)
	}

	declared, err := ParseDeclaredServices(dir)
	if err != nil {
		t.Fatalf("ParseDeclaredServices: %v", err)
	}
	if got := serviceNames(declared); len(got) != 2 {
		t.Fatalf("declared services = %v, want both", got)
	}
	for _, d := range declared {
		if d.ModulePath != "example.com/app" {
			t.Errorf("%s: ModulePath = %q, want it set from go.mod", d.Name, d.ModulePath)
		}
	}
	if got := serviceNames(UnmanagedServices(declared)); len(got) != 1 || got[0] != "ChatService" {
		t.Errorf("UnmanagedServices = %v, want [ChatService]", got)
	}
}

// IntrospectComponents is what composition, mounts, cmd subcommands, the
// workloads declaration and the audit inventory are built from. An unmanaged
// service must not appear as a server component, or compose.go would wire a
// Mount for a handler package that does not exist.
func TestIntrospectComponents_SkipsUnmanagedServices(t *testing.T) {
	dir := writeDescriptorProject(t,
		ServiceDef{Name: "ChatService", Package: "app.v1", Unmanaged: true},
		ServiceDef{Name: "BillingService", Package: "app.v1"},
	)
	comps := IntrospectComponents(dir)
	if len(comps) != 1 || comps[0].Name != "billing" {
		t.Fatalf("components = %+v, want only billing", comps)
	}
}

func TestCommentMarksUnmanaged(t *testing.T) {
	cases := []struct {
		comment string
		want    bool
	}{
		{" forge:unmanaged\n", true},
		{" forge:unmanaged — handlers live in internal/grpc\n", true},
		{" ChatService streams messages.\n forge:unmanaged\n", true},
		{"forge:unmanaged", true},
		// Prose that merely mentions it mid-line is not the marker.
		{" See forge:unmanaged in the docs.\n", false},
		// A longer word that starts with it is not it.
		{" forge:unmanagedness\n", false},
		{" forge:unmanage\n", false},
		{"", false},
	}
	for _, c := range cases {
		if got := CommentMarksUnmanaged(c.comment); got != c.want {
			t.Errorf("CommentMarksUnmanaged(%q) = %v, want %v", c.comment, got, c.want)
		}
	}
}

// A misspelled forge:unmanaged leaves the service managed, and generate then
// scaffolds a stub per rpc — so near misses are caught, while the real
// marker, other known markers and unrelated tokens are not.
func TestUnmanagedMarkerNearMiss(t *testing.T) {
	cases := []struct {
		comment string
		want    string
	}{
		{" forge:unmanged\n", "forge:unmanged"},
		{" forge:unmanage\n", "forge:unmanage"},
		{" forge:un-managed\n", "forge:un-managed"},
		{" forge:unmanaged\n", ""},
		{" forge:mutation\n", ""},
		{" forge:entity is for messages\n", ""},
		{" see forge:lint-disable-next-line\n", ""},
		{" plain prose\n", ""},
	}
	for _, c := range cases {
		if got := UnmanagedMarkerNearMiss(c.comment); got != c.want {
			t.Errorf("UnmanagedMarkerNearMiss(%q) = %q, want %q", c.comment, got, c.want)
		}
	}
}

// The raw scanner reads the marker with the entity markers' placement rule
// (comment and blank lines may sit between it and `service X {`; any
// declaration in between drops it) and attributes each rpc to its service,
// which is how source-scanning passes — the list-filter lint, the scaffold
// sweep — tell an unmanaged service's rpcs apart.
func TestScanRawProto_UnmanagedServicesAndRPCAttribution(t *testing.T) {
	dir := t.TempDir()
	src := `syntax = "proto3";
package app.v1;

// forge:unmanaged — still served by internal/grpc.

// ChatService streams chat messages.
service ChatService {
  rpc ListChats(ListChatsRequest) returns (ListChatsResponse);
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse) {
    option idempotency_level = IDEMPOTENT;
  }
}

// forge:unmanaged
message NotAService {}

service BillingService { rpc ListInvoices(ListInvoicesRequest) returns (ListInvoicesResponse); }
`
	if err := os.WriteFile(filepath.Join(dir, "app.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanRawProtoDir(dir)
	if err != nil {
		t.Fatalf("ScanRawProtoDir: %v", err)
	}
	if len(scan.Services) != 2 {
		t.Fatalf("services = %+v, want ChatService and BillingService", scan.Services)
	}
	if !scan.ServiceUnmanaged("ChatService") {
		t.Error("ChatService: the marker above it (across a blank line and a doc comment) was not read")
	}
	if scan.ServiceUnmanaged("BillingService") {
		t.Error("BillingService: a marker consumed by an intervening message must not carry over")
	}
	want := map[string]string{
		"ListChats":    "ChatService",
		"SendMessage":  "ChatService",
		"ListInvoices": "BillingService",
	}
	for _, rpc := range scan.RPCs {
		if want[rpc.Name] != rpc.Service {
			t.Errorf("rpc %s attributed to %q, want %q", rpc.Name, rpc.Service, want[rpc.Name])
		}
		delete(want, rpc.Name)
	}
	if len(want) != 0 {
		t.Errorf("rpcs not captured: %v", want)
	}
}
