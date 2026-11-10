// Copyright (c) 2025 Reliant Labs
package buildmode_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// Production alerting is assembled by forge from the alert-service parts under
// deploy/observability/spec (one per RPC service, read by a glob, never listed
// anywhere else). A service a process serves but that has no part, or whose
// part names another service, gets no default alerts in prod, and nothing else
// would notice. So every service the generated catalog says a process serves
// must have exactly one part, named after the service, whose rpc_service is
// that service; and no part may name a service nothing serves.
//
// Both files are read here, so the test cache notices an edit to either.

type alertServicePart struct {
	Version string `json:"version"`
	Alert   struct {
		Name       string `json:"name"`
		RPCService string `json:"rpc_service"`
	} `json:"alert"`
}

// alertServiceName is the part name forge derives from a fully qualified
// service: reliant.v1.WorktreeReclaimService -> worktree_reclaim.
func alertServiceName(service string) string {
	short := service[strings.LastIndex(service, ".")+1:]
	short = strings.TrimSuffix(short, "Service")
	// An acronym stays one word: MCPService -> mcp, HTTPProxy -> http_proxy.
	rs := []rune(short)
	var b strings.Builder
	for i, r := range rs {
		if unicode.IsUpper(r) {
			startsWord := i > 0 && (unicode.IsLower(rs[i-1]) ||
				(i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1])))
			if startsWord {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestAlertServiceName(t *testing.T) {
	for in, want := range map[string]string{
		"reliant.v1.WorktreeReclaimService": "worktree_reclaim",
		"reliant.v1.ChatService":            "chat",
		"reliant.v1.ToolsDaemonService":     "tools_daemon",
		"reliant.v1.MCPService":             "mcp",
		"reliant.v1.HTTPProxyService":       "http_proxy",
	} {
		if got := alertServiceName(in); got != want {
			t.Errorf("alertServiceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func servedServices(t *testing.T, root string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "gen", "forge_service_catalog.json"))
	if err != nil {
		t.Fatalf("read service catalog: %v", err)
	}
	var catalog struct {
		Processes []struct {
			ID     string `json:"id"`
			Serves []struct {
				Service string `json:"service"`
			} `json:"serves"`
		} `json:"processes"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("decode service catalog: %v", err)
	}
	served := map[string]bool{}
	for _, p := range catalog.Processes {
		for _, s := range p.Serves {
			served[s.Service] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("service catalog lists no served services; the catalog's shape changed")
	}
	return served
}

func alertServiceParts(t *testing.T, root string) map[string]alertServicePart {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "deploy", "observability", "spec", "alert-service-*_gen.json"))
	if err != nil {
		t.Fatalf("glob alert-service parts: %v", err)
	}
	parts := map[string]alertServicePart{}
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		var p alertServicePart
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode %s: %v", m, err)
		}
		parts[filepath.Base(m)] = p
	}
	return parts
}

func TestEveryServedServiceHasItsAlertInventoryPart(t *testing.T) {
	root := repoRoot(t)
	served := servedServices(t, root)
	parts := alertServiceParts(t, root)

	byService := map[string][]string{}
	for file, p := range parts {
		if p.Version != "forge.dashboard-parts/v2" {
			t.Errorf("%s: version %q, want forge.dashboard-parts/v2", file, p.Version)
		}
		want := "alert-service-" + p.Alert.Name + "_gen.json"
		if file != want {
			t.Errorf("%s: alert name %q does not match its file name (want %s)", file, p.Alert.Name, want)
		}
		if name := alertServiceName(p.Alert.RPCService); name != p.Alert.Name {
			t.Errorf("%s: name %q is not the one derived from rpc_service %q (%q)", file, p.Alert.Name, p.Alert.RPCService, name)
		}
		if !served[p.Alert.RPCService] {
			t.Errorf("%s: rpc_service %q is not served by any process in gen/forge_service_catalog.json", file, p.Alert.RPCService)
		}
		byService[p.Alert.RPCService] = append(byService[p.Alert.RPCService], file)
	}

	var services []string
	for s := range served {
		services = append(services, s)
	}
	sort.Strings(services)
	for _, s := range services {
		switch files := byService[s]; len(files) {
		case 1:
		case 0:
			t.Errorf("%s is served but has no deploy/observability/spec/alert-service-%s_gen.json: run task generate:forge", s, alertServiceName(s))
		default:
			sort.Strings(files)
			t.Errorf("%s has %d alert-service parts, want 1: %v", s, len(files), files)
		}
	}

	// The service this inventory part was added for, named explicitly so the
	// check cannot pass vacuously if the catalog stops listing it.
	if p, ok := parts["alert-service-worktree_reclaim_gen.json"]; !ok || p.Alert.RPCService != "reliant.v1.WorktreeReclaimService" {
		t.Errorf("alert-service-worktree_reclaim_gen.json missing or not bound to reliant.v1.WorktreeReclaimService: %+v", p)
	}
}
