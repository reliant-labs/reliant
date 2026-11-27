//go:build e2e

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/naming"
)

// TestE2EGeneratedHooksReadTheirTransportScope is the runtime gate on
// forge#2726 (F4b): a generated hook file resolves its client from the
// QueryClient the call runs on, so a subtree mounted in a TransportScope
// (@reliantlabs/forge-web-runtime/transport-scope) reads the scope's
// transport through the SAME generated hooks, and nothing reaches the app's.
//
// Under the project's own vitest, with this checkout's web-runtime installed
// (the npm overlay in CI, the --link-forge bridge locally), and an app
// transport that records and refuses every call:
//
//   - a read hook, an infinite read hook and a mutation hook rendered inside
//     a TransportScopeProvider answer from the scope's transport, and the app
//     transport sees no call;
//   - `scope.queryClient.fetchQuery(<svc>Queries.<rpc>(req))` and
//     `<svc>Mutations(scope.queryClient).<rpc>(req)` reach the scope too,
//     outside React;
//   - the same read hook OUTSIDE a scope reaches the app's transport, as
//     every generated hook did before: the control that proves the refusing
//     app transport is really the one an unscoped call uses, so the "no live
//     call" half is not vacuous.
//
// tsc then typechecks the project with the generated file as emitted: the
// fourth call parameter and the /transport-scope import must compile against
// the runtime a scaffold installs.
//
// COST: one scaffold + generate, one tsc run, one vitest run (~2-4 min).
func TestE2EGeneratedHooksReadTheirTransportScope(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")

	forgeBin := buildforgeBinary(t)
	// `project new feapp --frontend web` + `scaffold service item`, shared
	// with the other web+item tests (shared_project_e2e_test.go).
	projectDir := newProjectFromSharedE2E(t, forgeBin, webItemProjectE2E)

	// A CRUD entity: ListItems is a paged read (a query hook and an infinite
	// one), CreateItem a write.
	protoPath := filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto")
	if err := os.WriteFile(protoPath, []byte(readFileE2E(t, protoPath)+`
// forge:entity
message Item {
  string id = 1;
  string name = 2;
}
`), 0o644); err != nil {
		t.Fatalf("declare the Item entity: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")
	proto := readFileE2E(t, protoPath)
	for _, rpc := range []string{"rpc CreateItem(", "rpc ListItems("} {
		if !strings.Contains(proto, rpc) {
			t.Fatalf("scaffold did not inject %q — without a read and a write the scope assertions are vacuous:\n%s", rpc, proto)
		}
	}
	runCmd(t, projectDir, forgeBin, "generate")

	webDir := filepath.Join(projectDir, "frontends", "web")
	hooksFile := naming.ServiceHookFile("ItemService")
	hooksModule := "@/hooks/" + strings.TrimSuffix(hooksFile, ".ts")

	// The artifact: one scoped resolver over the app-owned connectClient,
	// and every call going through it.
	hooks := readFileE2E(t, filepath.Join(webDir, "src", "hooks", hooksFile))
	for _, want := range []string{
		`import { scopedClient } from "@reliantlabs/forge-web-runtime/transport-scope";`,
		`import { connectClient } from "@/lib/connect";`,
		"const clientOf = scopedClient(ItemService, connectClient(ItemService));",
		"clientOf(context).listItems(req, { ...options, signal })",
		"clientOf(context).createItem(req, { ...options, signal })",
	} {
		if !strings.Contains(hooks, want) {
			t.Errorf("generated hooks are missing %q:\n%s", want, hooks)
		}
	}
	if strings.Contains(hooks, "const client = connectClient(") {
		t.Errorf("generated hooks still hold a module client every call uses:\n%s", hooks)
	}

	writeFileE2E(t, filepath.Join(webDir, "src", "app", "transportscope", "scope.test.tsx"),
		fmt.Sprintf(transportScopeFixture, hooksModule))
	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	runCmdTimeout(t, webDir, 5*time.Minute, "npx", "--no-install", "vitest", "run", "src/app/transportscope")
}

// transportScopeFixture runs under the project's vitest. %s is the generated
// hooks module specifier.
const transportScopeFixture = `import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { createTransportScope, TransportScopeProvider, type TransportScope } from "@reliantlabs/forge-web-runtime/transport-scope";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { itemServiceMutations, itemServiceQueries, useCreateItem, useListItems, useListItemsInfinite } from "%[1]s";

import type { Transport } from "@connectrpc/connect";
import type { ReactNode } from "react";

// The app's transport: every call it receives is recorded and refused, so a
// scoped call that leaked to it fails twice over.
let live: string[] = [];
const liveTransport: Transport = {
  unary: async (method) => {
    live.push(method.name);
    throw new Error("live call: " + method.name);
  },
  stream: async (method) => {
    live.push(method.name);
    throw new Error("live call: " + method.name);
  },
};

// The scope's transport answers with fixtures and records what it served.
function fixtureScope(): { scope: TransportScope; served: string[] } {
  const served: string[] = [];
  const scope = createTransportScope({
    id: "fixture",
    transport: mockTransport({
      "ItemService.ListItems": () => {
        served.push("ListItems");
        return { items: [{ id: "i1", name: "from the scope" }] };
      },
      "ItemService.CreateItem": () => {
        served.push("CreateItem");
        return { item: { id: "i2", name: "created in the scope" } };
      },
    }),
    queryClient: new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } }),
  });
  return { scope, served };
}

beforeEach(() => {
  live = [];
  setTransport(liveTransport);
});
afterEach(() => cleanup());

describe("generated hooks under a TransportScope", () => {
  it("read, infinite read and write hooks reach the scope's transport, never the app's", async () => {
    const { scope, served } = fixtureScope();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>
    );

    const list = renderHook(() => useListItems({}), { wrapper });
    const paged = renderHook(() => useListItemsInfinite({ pageSize: 5 }), { wrapper });
    await waitFor(() => {
      expect(list.result.current.data?.items[0]?.name).toBe("from the scope");
      expect(paged.result.current.data?.pages[0]?.items[0]?.name).toBe("from the scope");
    });

    const create = renderHook(() => useCreateItem(), { wrapper });
    const created = await create.result.current.mutateAsync({ name: "x" } as never);
    expect(created.item?.name).toBe("created in the scope");

    expect(served).toContain("CreateItem");
    expect(served.filter((m) => m === "ListItems").length).toBeGreaterThanOrEqual(2);
    expect(live).toEqual([]);
    scope.queryClient.clear();
  });

  it("the query options and mutations reach the scope outside React", async () => {
    const { scope, served } = fixtureScope();

    const fetched = await scope.queryClient.fetchQuery(itemServiceQueries.listItems({}));
    expect(fetched.items[0]?.name).toBe("from the scope");
    const created = await itemServiceMutations(scope.queryClient).createItem({ name: "x" } as never);
    expect(created.item?.name).toBe("created in the scope");

    expect(served).toEqual(["ListItems", "CreateItem"]);
    expect(live).toEqual([]);
    scope.queryClient.clear();
  });

  it("outside a scope the same hook reaches the app's transport, as before", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const list = renderHook(() => useListItems({}), { wrapper });
    await waitFor(() => expect(list.result.current.isError).toBe(true));
    expect(live).toEqual(["ListItems"]);
    client.clear();
  });
});
`
