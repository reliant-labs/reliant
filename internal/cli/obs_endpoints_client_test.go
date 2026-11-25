package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// endpointsClientRunner answers `obs endpoints`' two panels apart: the
// server endpoints, and the browser's forge.rpc.client spans (known by the
// attribute key bound as a parameter).
type endpointsClientRunner struct {
	mu        sync.Mutex
	ran       []obsquery.Stmt
	clientErr error
}

func (r *endpointsClientRunner) Run(_ context.Context, st obsquery.Stmt, _ obsbackend.Limits) (obsbackend.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, st)
	for _, v := range st.Params {
		if v == "forge.rpc.client" {
			if r.clientErr != nil {
				return obsbackend.Result{}, r.clientErr
			}
			return obsbackend.Result{Rows: [][]any{
				{[]any{"todo.v1.TodoService", "CreateTodo"}, jn("450"), jn("42")},
				{[]any{"billing.v1.BillingService", "Charge"}, jn("9"), jn("700")},
			}}, nil
		}
	}
	row := func(name, req string) []any {
		return []any{[]any{"api", name}, jn(req), jn("0"), jn("1"), jn("5"), jn("9")}
	}
	return obsbackend.Result{Rows: [][]any{row("todo.v1.TodoService/CreateTodo", "300"), row("GET /healthz", "100")}}, nil
}

func TestObsEndpointsClientP95(t *testing.T) {
	out, errOut, err := obsRun(t, devFacts(), &endpointsClientRunner{}, "endpoints", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	eps := doc["endpoints"].([]any)
	if len(eps) != 2 {
		t.Fatalf("endpoints %v", eps)
	}
	byName := map[string]map[string]any{}
	for _, e := range eps {
		m := e.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if e := byName["todo.v1.TodoService/CreateTodo"]; e["client_p95_ms"] != 42.0 || e["client_calls"] != 450.0 {
		t.Errorf("a called rpc carries its client p95: %v", e)
	}
	if e := byName["GET /healthz"]; e["client_p95_ms"] != nil || e["client_calls"] != nil {
		t.Errorf("an endpoint no browser called has no client fields: %v", e)
	}
	if _, ok := doc["client_note"]; ok || errOut != "" {
		t.Errorf("no note when the client read succeeded: %v %q", doc["client_note"], errOut)
	}

	out, _, err = obsRun(t, devFacts(), &endpointsClientRunner{}, "endpoints", "dev", "--json", "--fields", "endpoints.name,endpoints.client_p95_ms,endpoints.client_calls")
	if err != nil {
		t.Fatal(err)
	}
	if e := decode(t, out)["endpoints"].([]any)[0].(map[string]any); len(e) != 3 || e["client_p95_ms"] != 42.0 {
		t.Errorf("--fields selects the client columns: %v", e)
	}

	out, _, err = obsRun(t, devFacts(), &endpointsClientRunner{}, "endpoints", "dev")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[0], "CLIENT P95") {
		t.Fatalf("table header: %q", lines[0])
	}
	for _, l := range lines {
		switch {
		case strings.Contains(l, "CreateTodo") && !strings.HasSuffix(strings.TrimSpace(l), "42ms"):
			t.Errorf("CreateTodo's client p95: %q", l)
		case strings.Contains(l, "GET /healthz") && !strings.HasSuffix(strings.TrimSpace(l), "-"):
			t.Errorf("no client spans is a dash: %q", l)
		}
	}
}

// The client read is an enrichment: when it fails the verb still answers,
// exit 0, with the endpoints and a note saying why the column is empty.
func TestObsEndpointsClientFailureIsANote(t *testing.T) {
	r := &endpointsClientRunner{clientErr: errors.New("Code: 47. Unknown identifier")}
	out, errOut, err := obsRun(t, devFacts(), r, "endpoints", "dev", "--json")
	if err != nil {
		t.Fatalf("the enrichment failed the verb: %v", err)
	}
	doc := decode(t, out)
	note, _ := doc["client_note"].(string)
	if doc["ok"] != true || len(doc["endpoints"].([]any)) != 2 || !strings.HasPrefix(note, "client p95 unavailable:") ||
		!strings.Contains(note, "Unknown identifier") || !strings.Contains(errOut, "note: client p95 unavailable") {
		t.Errorf("doc %v, stderr %q", doc, errOut)
	}
	if _, ok := doc["endpoints"].([]any)[0].(map[string]any)["client_p95_ms"]; ok {
		t.Error("a failed client read leaves the column empty")
	}
}

func TestObsEndpointsExplainListsTheClientStatement(t *testing.T) {
	r := &endpointsClientRunner{}
	out, _, err := obsRun(t, devFacts(), r, "endpoints", "dev", "--explain")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 0 || !strings.Contains(out, "-- endpoints client") || !strings.Contains(out, "forge.rpc.client") {
		t.Errorf("explain ran %d statements:\n%s", len(r.ran), out)
	}
}
