package forgeconv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lintListFilters runs the whole proto convention pass over one file in a
// fresh tree and returns this rule's findings — the rule is tree-level (enums
// resolve across files), so it is exercised through LintProtoTree rather than
// the per-file entry point.
func lintListFilters(t *testing.T, name, src string) []Finding {
	t.Helper()
	dir := t.TempDir()
	must(t, writeFile(filepath.Join(dir, name), src))
	res, err := LintProtoTree(dir)
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	return findingsForRule(res.Findings, ruleListFilterOptional)
}

// The shape that shipped in control-plane: a List request whose scope field
// was a plain `string`. `forge lint` said nothing; the born list page called
// the hook with no org_id and the server rejected every load. forge's own
// documented rule — "List request filter fields must be optional" — had no
// check behind it.
const listUsageEventsProto = `syntax = "proto3";

package billing.v1;

import "google/protobuf/timestamp.proto";

service BillingService {
  rpc ListUsageEvents(ListUsageEventsRequest) returns (ListUsageEventsResponse);
}

message ListUsageEventsRequest {
  string org_id = 1;
  google.protobuf.Timestamp period_start = 2;
  google.protobuf.Timestamp period_end = 3;
  int32 page_size = 4;
  string page_token = 5;
}

message ListUsageEventsResponse {
  repeated UsageEvent usage_events = 1;
  string next_page_token = 2;
}

message UsageEvent {
  string id = 1;
  string org_id = 2;
}
`

func TestListFilterOptional_FlagsNonOptionalScalarFilter(t *testing.T) {
	got := lintListFilters(t, "billing.proto", listUsageEventsProto)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 finding (org_id), got %d: %+v", len(got), got)
	}
	f := got[0]
	if f.Severity != SeverityError {
		t.Errorf("severity = %s, want error (matches the other forgeconv proto rules)", f.Severity)
	}
	if f.Line != 12 {
		t.Errorf("line = %d, want 12 (the org_id declaration)", f.Line)
	}
	if !strings.Contains(f.Message, "ListUsageEventsRequest.org_id") {
		t.Errorf("message should name the field: %s", f.Message)
	}
	if !strings.Contains(f.Remediation, "optional string org_id = 1;") {
		t.Errorf("remediation should spell the fix: %s", f.Remediation)
	}
}

// Every scalar kind lacks presence, so every one is flagged — bool and enum
// are the worst (the generated op always applies them, so an omitted filter
// returns only the false / UNSPECIFIED rows).
func TestListFilterOptional_FlagsEveryScalarKindIncludingEnums(t *testing.T) {
	src := `syntax = "proto3";
package orders.v1;

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_SHIPPED = 1;
}

message ListOrdersRequest {
  bool expedited = 1;
  OrderStatus status = 2;
  int64 customer_number = 3;
  string search = 4;
  Order.Priority priority = 5;
}

message Order {
  enum Priority {
    PRIORITY_UNSPECIFIED = 0;
  }
  string id = 1;
}
`
	got := lintListFilters(t, "orders.proto", src)
	var names []string
	for _, f := range got {
		names = append(names, f.Message)
	}
	for _, want := range []string{".expedited ", ".status ", ".customer_number ", ".search ", ".priority "} {
		found := false
		for _, n := range names {
			if strings.Contains(n, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a finding for %q; got %q", want, names)
		}
	}
	if len(got) != 5 {
		t.Errorf("expected 5 findings, got %d: %q", len(got), names)
	}
}

// Negative cases: fields that already carry presence, fields that are not
// filters at all, and messages that are not List requests.
func TestListFilterOptional_IgnoresFieldsWithPresenceAndNonListMessages(t *testing.T) {
	src := `syntax = "proto3";
package widgets.v1;

import "google/protobuf/timestamp.proto";

service WidgetService {
  rpc ListWidgets(ListWidgetsRequest) returns (ListWidgetsResponse);
  rpc GetWidget(GetWidgetRequest) returns (GetWidgetResponse);
  rpc CreateWidget(CreateWidgetRequest) returns (CreateWidgetResponse);
}

message ListWidgetsRequest {
  optional string search = 1;
  optional bool active = 2;
  google.protobuf.Timestamp created_after = 3;
  WidgetScope scope = 4;
  repeated string tags = 5;
  map<string, string> labels = 6;
  oneof owner {
    string user_id = 7;
    string team_id = 8;
  }
  // forge-style multi-line annotation on an optional field
  optional string region = 9 [
    deprecated = true
  ];
}

message WidgetScope {
  string region = 1;
}

message ListWidgetsResponse {
  repeated Widget widgets = 1;
  string next_page_token = 2;
  int32 total_count = 3;
}

message GetWidgetRequest { string id = 1; }
message GetWidgetResponse { Widget widget = 1; }
message CreateWidgetRequest { string name = 1; bool active = 2; }
message CreateWidgetResponse { Widget widget = 1; }

message Widget {
  string id = 1;
  string name = 2;
}
`
	if got := lintListFilters(t, "widgets.proto", src); len(got) != 0 {
		t.Fatalf("expected no findings, got %d: %+v", len(got), got)
	}
}

// Pagination and ordering are request controls, not filters: forge's
// generator never filters on them, and demanding `optional` on them would be
// a fix nobody should apply.
func TestListFilterOptional_ExcludesPaginationAndOrdering(t *testing.T) {
	src := `syntax = "proto3";
package widgets.v1;

message ListWidgetsRequest {
  int32 page_size = 1;
  string page_token = 2;
  string order_by = 3;
  bool descending = 4;
  bool desc = 5;
  string sort_order = 6;
  string cursor = 7;
  int32 limit = 8;
  int32 offset = 9;
}
`
	if got := lintListFilters(t, "widgets.proto", src); len(got) != 0 {
		t.Fatalf("pagination/ordering fields must not be flagged, got %d: %+v", len(got), got)
	}
}

// A field that declares itself required is a parameter, not a filter, and
// its zero value is rejected on the wire — so the unset/zero ambiguity the
// rule exists to remove cannot happen. Both protovalidate spellings count;
// a commented-out declaration of the same field must not be mistaken for the
// real one when the finding is located.
func TestListFilterOptional_RequiredParameterIsNotAFilter(t *testing.T) {
	src := `syntax = "proto3";
package billing.v1;

import "buf/validate/validate.proto";

message ListUsageEventsRequest {
  string org_id = 1 [(buf.validate.field).required = true];
  string project_id = 2 [(buf.validate.field) = {
    required: true
  }];
  // string region = 3;  (old spelling, kept for reference)
  string region = 3;
}
`
	got := lintListFilters(t, "billing.proto", src)
	if len(got) != 1 || !strings.Contains(got[0].Message, "ListUsageEventsRequest.region") {
		t.Fatalf("expected only region to be flagged, got %+v", got)
	}
	if got[0].Line != 12 {
		t.Errorf("line = %d, want 12 (the real declaration, not the comment above it)", got[0].Line)
	}
}

// Single-line bodies are how the frontend/pages skill writes its examples,
// so the rule must read them — and locate the field on its line.
func TestListFilterOptional_ReadsSingleLineMessages(t *testing.T) {
	src := `syntax = "proto3";
package users.v1;

message ListUsersRequest   { int32 page_size = 1; string page_token = 2; optional string search = 3; bool active = 4; }
message ListUsersResponse  { repeated string ids = 1; string next_page_token = 2; }
`
	got := lintListFilters(t, "users.proto", src)
	if len(got) != 1 || !strings.Contains(got[0].Message, "ListUsersRequest.active") || got[0].Line != 4 {
		t.Fatalf("expected one finding for ListUsersRequest.active on line 4, got %+v", got)
	}
	if !strings.Contains(got[0].Remediation, "optional bool active = 4;") {
		t.Errorf("remediation should spell the fix: %s", got[0].Remediation)
	}
}

// A List RPC's request is a List request whatever it is named: the generator
// keys the list op off the RPC, so the lint does too.
func TestListFilterOptional_FlagsInputOfAListRPCRegardlessOfName(t *testing.T) {
	src := `syntax = "proto3";
package widgets.v1;

service WidgetService {
  rpc ListWidgets(WidgetQuery) returns (WidgetPage) {
    option (forge.v1.method) = { auth_required: true };
  }
}

message WidgetQuery {
  int32 page_size = 1;
  bool archived = 2;
}

message WidgetPage {
  repeated string ids = 1;
}
`
	got := lintListFilters(t, "widgets.proto", src)
	if len(got) != 1 || !strings.Contains(got[0].Message, "WidgetQuery.archived") {
		t.Fatalf("expected one finding for WidgetQuery.archived, got %+v", got)
	}
}

// An enum declared in a sibling file (the shared types file is the usual
// home) is still an enum: the tree walk resolves enum names across files. A
// type the tree cannot resolve at all (an import from outside proto/) is
// assumed to be a message, which has presence — the safe direction.
func TestListFilterOptional_ResolvesEnumsAcrossTheTree(t *testing.T) {
	dir := t.TempDir()
	must(t, mkdirAll(filepath.Join(dir, "shared", "v1")))
	must(t, mkdirAll(filepath.Join(dir, "services", "jobs", "v1")))
	must(t, writeFile(filepath.Join(dir, "shared", "v1", "types.proto"), `syntax = "proto3";
package shared.v1;

enum JobState {
  JOB_STATE_UNSPECIFIED = 0;
  JOB_STATE_DONE = 1;
}
`))
	must(t, writeFile(filepath.Join(dir, "services", "jobs", "v1", "jobs.proto"), `syntax = "proto3";
package services.jobs.v1;

import "shared/v1/types.proto";
import "vendor/money.proto";

service JobsService {
  rpc ListJobs(ListJobsRequest) returns (ListJobsResponse);
}

message ListJobsRequest {
  shared.v1.JobState state = 1;
  vendor.Money min_cost = 2;
}

message ListJobsResponse {}
`))
	res, err := LintProtoTree(dir)
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	got := findingsForRule(res.Findings, ruleListFilterOptional)
	if len(got) != 1 || !strings.Contains(got[0].Message, "ListJobsRequest.state") {
		t.Fatalf("expected one finding for the cross-file enum filter, got:\n%s", res.FormatText())
	}
	if got[0].File != filepath.Join("services", "jobs", "v1", "jobs.proto") || got[0].Line != 12 {
		t.Errorf("finding location = %s:%d, want services/jobs/v1/jobs.proto:12", got[0].File, got[0].Line)
	}
}

// A required scope field (an org id the handler insists on) is the one
// legitimate non-optional field on a List request. It takes the ordinary
// reasoned suppression, and the reason documents why the field is not a
// filter.
func TestListFilterOptional_HonorsReasonedSuppression(t *testing.T) {
	dir := t.TempDir()
	src := strings.Replace(listUsageEventsProto, "  string org_id = 1;\n",
		"  // forge:lint-disable-next-line forgeconv-list-filter-optional: required org scope, enforced by the handler\n  string org_id = 1;\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "billing.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := LintProtoTree(dir)
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a reasoned suppression must silence the finding:\n%s", res.FormatText())
	}
}

// The rule's premise is forge's GENERATED list filter, and forge generates
// nothing for a `// forge:unmanaged` service: its hand-written handlers read
// the request however they like. So a request only an unmanaged service uses
// is not judged — the finding arrives when the service is adopted. A request
// a managed service ALSO uses keeps the finding, because that service's
// generated filter does read it.
func TestListFilterOptional_SkipsRequestsOnlyUnmanagedServicesUse(t *testing.T) {
	unmanaged := `syntax = "proto3";

package legacy.v1;

// forge:unmanaged — handlers still live in internal/grpc.
service LegacyService {
  rpc ListThings(ListThingsRequest) returns (ListThingsResponse);
}

message ListThingsRequest {
  string org_id = 1;
}

message ListThingsResponse {}
`
	if got := lintListFilters(t, "legacy.proto", unmanaged); len(got) != 0 {
		t.Fatalf("unmanaged service's list request was judged: %+v", got)
	}

	// Same file without the marker: the finding is back.
	managed := strings.Replace(unmanaged, "// forge:unmanaged — handlers still live in internal/grpc.\n", "", 1)
	if got := lintListFilters(t, "legacy.proto", managed); len(got) != 1 {
		t.Fatalf("managed service: want 1 finding, got %d: %+v", len(got), got)
	}

	// Shared by an unmanaged and a managed service: still judged.
	shared := unmanaged + `
service ModernService {
  rpc ListThings(ListThingsRequest) returns (ListThingsResponse);
}
`
	if got := lintListFilters(t, "legacy.proto", shared); len(got) != 1 {
		t.Fatalf("request shared with a managed service: want 1 finding, got %d: %+v", len(got), got)
	}
}

// A `// forge:custom` List rpc is exempt for the unmanaged service's reason:
// forge generates no list filter and no page for it, so its handler reads
// the request however it likes. reliant's ListTasks takes a plain project_id
// it requires; marked custom, that is the author's contract to state.
func TestListFilterOptional_SkipsRequestsOnlyCustomRPCsUse(t *testing.T) {
	custom := `syntax = "proto3";

package tasks.v1;

service TaskService {
  // forge:custom — lists a project's tasks with their dependency edges.
  rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
}

message ListTasksRequest {
  string project_id = 1;
}

message ListTasksResponse {}
`
	if got := lintListFilters(t, "tasks.proto", custom); len(got) != 0 {
		t.Fatalf("forge:custom List rpc's request was judged: %+v", got)
	}
	unmarked := strings.Replace(custom, "  // forge:custom — lists a project's tasks with their dependency edges.\n", "", 1)
	if got := lintListFilters(t, "tasks.proto", unmarked); len(got) != 1 {
		t.Fatalf("unmarked List rpc: want 1 finding, got %d: %+v", len(got), got)
	}
}
