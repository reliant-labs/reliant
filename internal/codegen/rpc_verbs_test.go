package codegen

import (
	"slices"
	"testing"

	"github.com/reliant-labs/forge/pkg/activity"
)

func TestReadVerb(t *testing.T) {
	for name, want := range map[string]string{
		// the hook generator's read verbs
		"GetOrder": "Get", "ListOrders": "List", "SearchOrders": "Search", "FindSeat": "Find",
		"CheckHealth": "Check", "HasAccess": "Has", "IsEnabled": "Is", "CountOrders": "Count",
		"ExistsOrder": "Exists",
		// the activity interceptor's, BatchGet its only batch form
		"WatchOrders": "Watch", "QueryUsage": "Query", "DescribeCluster": "Describe",
		"BatchGetOrders": "BatchGet",
		// no other Batch/Bulk prefix is stripped
		"BatchSearchOrders": "", "BatchUpdateOrders": "", "BulkGetOrders": "", "BulkListOrders": "",
		// a bare verb is a whole word
		"Get": "Get", "Get2Orders": "Get",
		// whole words only
		"CheckoutCart": "", "IssueRefund": "", "Listen": "", "Countersign": "", "Hash": "", "Getaway": "",
		// not a read verb at all
		"Health": "", "CreateOrder": "", "Batch": "", "BatchCreate": "",
	} {
		if got := ReadVerb(name); got != want {
			t.Errorf("ReadVerb(%q) = %q, want %q", name, got, want)
		}
	}
}

// The lint asks NO_SIDE_EFFECTS of exactly the names forge's own classifiers
// read: every hook-generator read is a ReadVerb read, and nothing else is
// except the activity interceptor's reads. The batch forms the hook
// generator does not read (BatchUpdate, BatchSearch, BulkGet) are not lint
// reads either.
func TestReadVerbAgreesWithTheHookGenerator(t *testing.T) {
	for _, name := range []string{
		"BatchUpdateOrders", "BatchSearchOrders", "BulkGetOrders",
		"GetOrder", "ListOrders", "SearchOrders", "CheckHealth", "IsEnabled", "ExistsOrder",
		"CheckoutCart", "IssueRefund", "Listen", "CreateOrder", "Health",
	} {
		hookRead := isQueryRPC(Method{Name: name})
		if lintRead := ReadVerb(name) != ""; lintRead != hookRead {
			t.Errorf("%s: the lint reads it as a read = %v, the hook generator = %v", name, lintRead, hookRead)
		}
	}
	for _, name := range []string{"BatchGetOrders", "WatchOrders", "QueryUsage", "DescribeCluster"} {
		if ReadVerb(name) == "" || activity.IsMutatingName(name) {
			t.Errorf("%s: an activity-interceptor read must be a lint read", name)
		}
	}
}

func TestWriteVerb(t *testing.T) {
	for name, want := range map[string]string{
		"CreateOrder": "Create", "UpdateOrder": "Update", "DeleteOrder": "Delete", "SetFlag": "Set",
		"CancelRun": "Cancel", "SendMessage": "Send", "UpsertRow": "Upsert", "RevokeKey": "Revoke",
		"Delete": "Delete",
		// whole words only
		"SettingsSummary": "", "Addresses": "", "Movement": "",
		// no Batch/Bulk prefix is stripped
		"BulkDeleteOrders": "", "BatchCreateOrders": "",
		// verbs a read-only rpc can open with are not judged
		"RunSavedQuery": "", "ExecuteQuery": "", "EnsureFresh": "", "StartPreview": "",
		"GetOrder": "", "ValidateOrder": "", "PreviewPromotion": "", "ExportOrders": "", "SyncStatus": "",
	} {
		if got := WriteVerb(name); got != want {
			t.Errorf("WriteVerb(%q) = %q, want %q", name, got, want)
		}
	}
}

// No verb is both: a name WriteVerb claims can never be one ReadVerb asks to
// declare NO_SIDE_EFFECTS, or the two lint rules would contradict.
func TestReadAndWriteVerbsAreDisjoint(t *testing.T) {
	if !slices.IsSorted(rpcWriteVerbs) {
		t.Error("rpcWriteVerbs is not sorted")
	}
	for _, v := range rpcWriteVerbs {
		if r := ReadVerb(v + "Thing"); r != "" {
			t.Errorf("write verb %s is also a read verb (%s)", v, r)
		}
	}
}
