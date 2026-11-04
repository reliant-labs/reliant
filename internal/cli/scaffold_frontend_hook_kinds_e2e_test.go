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

// TestE2EGeneratedHooksReadNoSideEffectsAndTypeMutationContext is the compile
// gate on two properties of the generated React Query hooks, against the real
// runtime as a scaffolded app installs it:
//
//   - an rpc declaring `option idempotency_level = NO_SIDE_EFFECTS;` is a
//     useQuery whatever its name. Version is a noun, not a read verb, so the
//     name heuristic alone generated a useMutation, and a caller had to
//     hand-build the query key the factory would have given it.
//   - a mutation hook's TContext is inferred from onMutate, so an optimistic
//     update reads its rollback off onError's context with no cast. The
//     hook used to pin TContext to `unknown`.
//   - a query hook's `select` sets its `data` type (TData), as in useQuery
//     itself. It used to be typed as returning the response, so a caller
//     mapping proto to a view model wrote a useMemo over `data` instead.
//   - a read in the list-pagination shape (the scaffolded ListItems) also
//     gets useListItemsInfinite, whose request has no pageToken (the hook
//     owns it) and whose `select` reshapes `{ pages }`.
//
// A HOOK-KIND FLIP under an owned starter test (reliant #1508): Version is
// born a mutation, so its starter row fires mutateAsync; declaring
// NO_SIDE_EFFECTS later makes it a useQuery. `forge generate` must print that
// row's line and new body, `forge lint --conventions` must fail on it, and
// the remediation both name (delete + `forge project rescaffold`) must clear
// it.
//
// A DECLARED ENTITY SCOPE: ItemStats carries `// forge:query-scope Item`, so
// its key sits under item and a CreateItem refetches it (vitest, below).
//
// THE RPC SCOPE: `itemServiceKeys.<rpc>.all` typechecks as an
// invalidateQueries / setQueriesData filter (and is not callable), and under
// vitest invalidating it refetches every request of that rpc — the scoped
// ItemStats, and ListItems' plain AND infinite reads — and no other rpc.
//
// QUERY OPTIONS OUTSIDE REACT: every read's `itemServiceQueries.<rpc>(req)`
// (and `.listItemsInfinite(req)`) typechecks in fetchQuery, ensureQueryData,
// prefetchQuery, fetchInfiniteQuery and useQueries with the response type
// inferred, and — under vitest — a fetchQuery fills the very cache entry the
// generated hook then reads, with one transport call.
//
// And one RUNTIME property, under the project's own vitest: the scaffolded
// mockTransport create()s both directions, like a real transport — a
// handler sees the full request and a partial fixture comes back as the full
// response — and the starter test drives the infinite hook against it.
//
// Positive AND negative, like the error-contract gate beside it: a positive
// half alone passes just as green when the hook is `any`. The negative half
// proves the kind (a query result has no `mutate`) and the context type (a
// field onMutate never returned is rejected) are load-bearing.
//
// COST: one scaffold + one `npm install` + two tsc runs + one vitest run
// (~3-5 min).
func TestE2EGeneratedHooksReadNoSideEffectsAndTypeMutationContext(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmdTimeout(t, dir, 10*time.Minute, forgeBin,
		"project", "new", "kindsapp",
		"--mod", "example.com/kindsapp",
		"--frontend", "web",
	)
	projectDir := filepath.Join(dir, "kindsapp")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "scaffold", "service", "item")

	// A CRUD entity for the mutation (CreateItem); a custom read named as a
	// noun (Version), born WITHOUT NO_SIDE_EFFECTS so it starts as a
	// mutation and its starter row is written for one; and a custom read of
	// items (ItemStats) that declares its entity scope.
	protoPath := filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto")
	proto := readFileE2E(t, protoPath)
	const rpcTODO = "  // TODO: Add your RPC methods here.\n"
	if !strings.Contains(proto, rpcTODO) {
		t.Fatalf("scaffolded item.proto no longer carries the RPC TODO anchor:\n%s", proto)
	}
	const versionRPC = "  rpc Version(VersionRequest) returns (VersionResponse);\n"
	proto = strings.Replace(proto, rpcTODO, versionRPC+
		"  // forge:query-scope Item — reads items, so an item write must refresh it.\n"+
		"  rpc ItemStats(ItemStatsRequest) returns (ItemStatsResponse) {\n    option idempotency_level = NO_SIDE_EFFECTS;\n  }\n", 1)
	proto += `
message VersionRequest {}

message VersionResponse {
  string version = 1;
}

message ItemStatsRequest {}

message ItemStatsResponse {
  int32 count = 1;
}

// forge:entity
message Item {
  string id = 1;
  string name = 2;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("write item.proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")
	if got := readFileE2E(t, protoPath); !strings.Contains(got, "rpc CreateItem(") {
		t.Fatalf("scaffold did not inject CreateItem — with no mutation the context half is vacuous:\n%s", got)
	}

	webDir := filepath.Join(projectDir, "frontends", "web")
	hooksFile := naming.ServiceHookFile("ItemService")
	hooksModule := "@/hooks/" + strings.TrimSuffix(hooksFile, ".ts")
	testRel := filepath.ToSlash(filepath.Join("frontends", "web", "src", "hooks",
		strings.TrimSuffix(strings.TrimSuffix(hooksFile, ".ts"), "_gen")+".test.tsx"))

	// ── THE FLIP: Version declares NO_SIDE_EFFECTS under an owned starter ─
	// The starter row was written for a mutation. Regenerating flips the
	// hook and leaves the row calling mutateAsync on a useQuery (reliant
	// #1508): generate must name the row, and the conventions lane must
	// fail on it until it is fixed.
	starter := readFileE2E(t, filepath.Join(projectDir, testRel))
	versionRow := strings.Index(starter, `it("useVersion resolves`)
	if versionRow < 0 || !strings.Contains(starter[versionRow:], "renderHook(() => useVersion(), { wrapper })") {
		t.Fatalf("precondition: the starter's Version row is a mutation row:\n%s", starter)
	}
	rowLine := strings.Count(starter[:versionRow], "\n") + 1
	proto = readFileE2E(t, protoPath)
	proto = strings.Replace(proto, versionRPC,
		"  rpc Version(VersionRequest) returns (VersionResponse) {\n    option idempotency_level = NO_SIDE_EFFECTS;\n  }\n", 1)
	writeFileE2E(t, protoPath, proto)
	genOut := runCmdKeep(t, projectDir, forgeBin, "generate")
	for _, want := range []string{
		"useVersion is now a useQuery (was a useMutation)",
		testRel + ": a scaffold-once starter test",
		fmt.Sprintf("line %d: useVersion is now a useQuery", rowLine),
		"renderHook(() => useVersion({} as never), { wrapper });",
	} {
		if !strings.Contains(genOut, want) {
			t.Errorf("generate did not report %q for the flipped hook:\n%s", want, genOut)
		}
	}
	lintOut, err := runCmdCombined(projectDir, 5*time.Minute, forgeBin, "lint", "--conventions", "--no-fix")
	if err == nil || !strings.Contains(lintOut, "forgeconv-frontend-hook-test-shape") ||
		!strings.Contains(lintOut, fmt.Sprintf("%s:%d", testRel, rowLine)) {
		t.Fatalf("`forge lint --conventions` must fail on the stale row at %s:%d (err %v):\n%s", testRel, rowLine, err, lintOut)
	}
	// The remediation both name: delete the file and rescaffold it.
	if err := os.Remove(filepath.Join(projectDir, testRel)); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "project", "rescaffold", testRel)
	if fresh := readFileE2E(t, filepath.Join(projectDir, testRel)); !strings.Contains(fresh, "renderHook(() => useVersion({} as never), { wrapper })") {
		t.Fatalf("rescaffold did not write a query row for useVersion:\n%s", fresh)
	}
	runCmd(t, projectDir, forgeBin, "lint", "--conventions", "--no-fix")

	// The classification, read off the artifact: Version is built by the
	// query factory and has a key-factory entry of its own.
	hooks := readFileE2E(t, filepath.Join(webDir, "src", "hooks", hooksFile))
	for _, want := range []string{
		"export const useVersion = createQueryHook<",
		`version: keys.query("version", VersionRequestSchema)`,
		"export const useCreateItem = createMutationHook<",
		"export const useListItemsInfinite = createInfiniteQueryHook<",
		`listItemsInfinite: keys.infinite("listItems", ListItemsRequestSchema, "item")`,
		// forge:query-scope Item: the custom read is keyed under item.
		`itemStats: keys.query("itemStats", ItemStatsRequestSchema, "item")`,
		// The hooks are built over the query options, never beside them.
		"export const itemServiceQueries = {",
		"  itemServiceQueries.version,\n",
		"  itemServiceQueries.listItemsInfinite,\n",
	} {
		if !strings.Contains(hooks, want) {
			t.Errorf("generated hooks are missing %q:\n%s", want, hooks)
		}
	}

	// ── POSITIVE: the read is a query, the rollback needs no cast ──────
	writeFileE2E(t, filepath.Join(webDir, "src", "app", "hookkinds", "page.tsx"),
		fmt.Sprintf(hookKindsPositiveFixture, hooksModule))
	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")

	// ── RUNTIME: full messages both ways, and the starter's infinite row ─
	writeFileE2E(t, filepath.Join(webDir, "src", "app", "hookkinds", "transport.test.tsx"), hookKindsTransportFixture)
	writeFileE2E(t, filepath.Join(webDir, "src", "app", "hookkinds", "scope.test.tsx"),
		fmt.Sprintf(hookKindsScopeFixture, hooksModule))
	writeFileE2E(t, filepath.Join(webDir, "src", "app", "hookkinds", "options.test.tsx"),
		fmt.Sprintf(hookKindsOptionsFixture, hooksModule))
	runCmdTimeout(t, webDir, 5*time.Minute, "npx", "--no-install", "vitest", "run",
		"src/app/hookkinds", "src/hooks/"+strings.TrimSuffix(strings.TrimSuffix(hooksFile, ".ts"), "_gen")+".test.tsx")

	// ── NEGATIVE: neither property may be `any` ─────────────────────────
	writeFileE2E(t, filepath.Join(webDir, "src", "app", "hookkinds", "negative.ts"),
		fmt.Sprintf(hookKindsNegativeFixture, hooksModule))
	out, err := runCmdCombined(webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	if err == nil {
		t.Fatalf("tsc accepted a query result's `mutate` and an undeclared context field — "+
			"the hooks are untyped and the positive half proves nothing:\n%s", out)
	}
	if !strings.Contains(out, "Property 'mutate' does not exist") {
		t.Errorf("tsc did not reject `mutate` on useVersion — the NO_SIDE_EFFECTS rpc is not a query hook:\n%s", out)
	}
	if !strings.Contains(out, "nonexistentRollback") {
		t.Errorf("tsc did not reject a field onMutate never returned — TContext is not inferred:\n%s", out)
	}
	if !strings.Contains(out, "'number | undefined' is not assignable to type 'string | undefined'") {
		t.Errorf("tsc did not reject select's number read as a string — data is not typed by select:\n%s", out)
	}
	if !strings.Contains(out, "pageToken") {
		t.Errorf("tsc accepted a pageToken on the infinite hook's request — the hook does not own the token:\n%s", out)
	}
	if !strings.Contains(out, "Type 'number' is not assignable to type 'string'") {
		t.Errorf("tsc accepted fetchQuery's number read as a string — the query options do not type the response:\n%s", out)
	}
	if !strings.Contains(out, "This expression is not callable") {
		t.Errorf("tsc accepted a call of itemServiceKeys.itemStats.all — the rpc scope is untyped:\n%s", out)
	}
}

// hookKindsPositiveFixture uses useVersion as a read (request in, data out)
// and rolls an optimistic CreateItem back through onError's typed context.
// %s is the generated hooks module specifier.
const hookKindsPositiveFixture = `"use client";

import { useQueries, useQueryClient } from "@tanstack/react-query";

import { itemServiceKeys, itemServiceQueries, useCreateItem, useListItems, useListItemsInfinite, useVersion } from "%[1]s";

// The rpc scope as a filter: every request of one rpc, with no predicate.
async function refreshStats(qc: ReturnType<typeof useQueryClient>): Promise<void> {
  await qc.invalidateQueries({ queryKey: itemServiceKeys.itemStats.all });
  qc.setQueriesData({ queryKey: itemServiceKeys.listItems.all }, (old: unknown) => old);
}

// Outside React: the query options go straight into the QueryClient.
async function warm(qc: ReturnType<typeof useQueryClient>): Promise<number> {
  await refreshStats(qc);
  const v = await qc.fetchQuery(itemServiceQueries.version({}));
  const label: string = v.version;
  const stats = await qc.ensureQueryData(itemServiceQueries.itemStats({}));
  await qc.prefetchQuery(itemServiceQueries.listItems({}));
  const pages = await qc.fetchInfiniteQuery(itemServiceQueries.listItemsInfinite({ pageSize: 20 }));
  await qc.prefetchInfiniteQuery(itemServiceQueries.listItemsInfinite({}));
  const cached = qc.getQueryData(itemServiceQueries.itemStats({}).queryKey);
  return stats.count + pages.pages.flatMap((p) => p.items).length + label.length + (cached?.count ?? 0);
}

export default function HookKindsPage() {
  const both = useQueries({
    queries: [itemServiceQueries.itemStats({}), itemServiceQueries.version({}, { select: (r) => r.version })],
  });
  const statCount: number = both[0].data?.count ?? 0;
  const versionLabel: string = both[1].data ?? "";
  void statCount;
  void versionLabel;
  const qc = useQueryClient();
  void (() => warm(qc));
  const version = useVersion({});
  const label: string = version.data?.version ?? "";
  const names = useListItems({}, { select: (r) => r.items.map((i) => i.name) });
  const first: string = names.data?.[0] ?? "";
  const paged = useListItemsInfinite({ pageSize: 20 }, { select: (d) => d.pages.flatMap((p) => p.items) });
  const count: number = paged.data?.length ?? 0;
  const create = useCreateItem({
    onMutate: (req) => ({ rollback: () => String(req.name) }),
    onError: (_err, _req, context) => {
      context?.rollback();
    },
  });
  return (
    <button onClick={() => (paged.hasNextPage ? paged.fetchNextPage() : create.mutate({ name: "x" }))}>
      {label} {first} {count}
    </button>
  );
}
`

// hookKindsNegativeFixture MUST NOT compile, once per property. %s is the
// generated hooks module specifier.
const hookKindsNegativeFixture = `import { QueryClient } from "@tanstack/react-query";

import { itemServiceKeys, itemServiceQueries, useCreateItem, useListItems, useListItemsInfinite, useVersion } from "%[1]s";

export async function bogusFetch(qc: QueryClient) {
  const notAString: string = (await qc.fetchQuery(itemServiceQueries.itemStats({}))).count;
  return notAString;
}

export const bogusScope = itemServiceKeys.itemStats.all({});

export function bogus() {
  const version = useVersion({});
  version.mutate({});
  const count = useListItems({}, { select: (r) => r.items.length });
  const wrong: string | undefined = count.data;
  useListItemsInfinite({ pageToken: "x" });
  useCreateItem({
    onMutate: () => ({ rollback: () => undefined }),
    onError: (_err, _req, context) => context?.nonexistentRollback(),
  });
  return wrong;
}
`

// hookKindsScopeFixture runs under the project's vitest: a CRUD write of
// Item (CreateItem, which invalidates [service, "item"]) refetches the
// custom read ItemStats because `// forge:query-scope Item` keyed it there.
// Unscoped, only `.all` reached it and it stayed stale. %s is the generated
// hooks module specifier.
const hookKindsScopeFixture = `import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { itemServiceKeys, useCreateItem, useItemStats, useListItems, useListItemsInfinite } from "%[1]s";

import type { ReactNode } from "react";

afterEach(() => cleanup());

describe("<svc>Keys.<rpc>.all", () => {
  it("invalidating it refetches every request of that rpc, plain and infinite, and no other rpc", async () => {
    const calls = { stats: 0, lists: 0 };
    setTransport(
      mockTransport({
        "ItemService.ItemStats": () => {
          calls.stats += 1;
          return { count: calls.stats };
        },
        "ItemService.ListItems": () => {
          calls.lists += 1;
          return { items: [] };
        },
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const stats = renderHook(() => useItemStats({}), { wrapper });
    const listA = renderHook(() => useListItems({ pageSize: 1 }), { wrapper });
    const listB = renderHook(() => useListItems({ pageSize: 2 }), { wrapper });
    const paged = renderHook(() => useListItemsInfinite({ pageSize: 3 }), { wrapper });
    await waitFor(() => {
      expect(stats.result.current.isSuccess).toBe(true);
      expect(listA.result.current.isSuccess).toBe(true);
      expect(listB.result.current.isSuccess).toBe(true);
      expect(paged.result.current.isSuccess).toBe(true);
    });
    expect(calls).toEqual({ stats: 1, lists: 3 });

    // ItemStats is query-scoped, so its method sits at index 2, not 1.
    await client.invalidateQueries({ queryKey: itemServiceKeys.itemStats.all });
    expect(calls).toEqual({ stats: 2, lists: 3 });

    await client.invalidateQueries({ queryKey: itemServiceKeys.listItems.all });
    expect(calls).toEqual({ stats: 2, lists: 6 });
    client.clear();
  });
});

describe("forge:query-scope", () => {
  it("refetches a scoped custom read after a CRUD write of its entity", async () => {
    let reads = 0;
    setTransport(
      mockTransport({
        "ItemService.ItemStats": () => {
          reads += 1;
          return { count: reads };
        },
        "ItemService.CreateItem": { response: {} },
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const stats = renderHook(() => useItemStats({}), { wrapper });
    await waitFor(() => expect(stats.result.current.data?.count).toBe(1));
    const create = renderHook(() => useCreateItem(), { wrapper });
    await create.result.current.mutateAsync({ name: "x" } as never);
    await waitFor(() => expect(stats.result.current.data?.count).toBe(2));
    client.clear();
  });
});
`

// hookKindsOptionsFixture runs under the project's vitest: a fetchQuery /
// fetchInfiniteQuery of the generated query options OUTSIDE React fills the
// exact cache entry the generated hook reads — the hook renders the fetched
// data at once and the transport is called once. That is the property a
// hand-copied `{ queryKey, queryFn }` loses when it drifts. %s is the
// generated hooks module specifier.
const hookKindsOptionsFixture = `import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { itemServiceQueries, useItemStats, useListItemsInfinite } from "%[1]s";

import type { ReactNode } from "react";

afterEach(() => cleanup());

describe("generated query options", () => {
  it("a fetchQuery outside React fills the entry the hook reads", async () => {
    let reads = 0;
    let lists = 0;
    setTransport(
      mockTransport({
        "ItemService.ItemStats": () => {
          reads += 1;
          return { count: 7 };
        },
        "ItemService.ListItems": () => {
          lists += 1;
          return { items: [{ id: "i1", name: "one" }] };
        },
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 60_000 } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );

    const fetched = await client.fetchQuery(itemServiceQueries.itemStats({}));
    expect(fetched.count).toBe(7);
    const stats = renderHook(() => useItemStats({}), { wrapper });
    expect(stats.result.current.data?.count).toBe(7);

    const paged = await client.fetchInfiniteQuery(itemServiceQueries.listItemsInfinite({ pageSize: 5 }));
    expect(paged.pages[0]?.items[0]?.name).toBe("one");
    const list = renderHook(() => useListItemsInfinite({ pageSize: 5 }), { wrapper });
    expect(list.result.current.data?.pages[0]?.items[0]?.name).toBe("one");

    expect(reads).toBe(1);
    expect(lists).toBe(1);
    client.clear();
  });
});
`

// hookKindsTransportFixture runs under the project's vitest: the scaffolded
// mockTransport must create() the request a handler sees and the response a
// caller gets, as a real transport serializes and parses them. Before, both
// were the caller's partial init shapes, so a handler reading
// req.<repeated>.length threw and a caller reading res.items.length did too.
const hookKindsTransportFixture = `import { createClient } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { ItemService } from "@/gen/services/item/v1/item_pb";
import { mockTransport } from "@/lib/test-utils";

describe("mockTransport", () => {
  it("hands the handler the full request and the caller the full response", async () => {
    let seenToken: string | undefined;
    const client = createClient(
      ItemService,
      mockTransport({
        "ItemService.ListItems": (req) => {
          seenToken = (req as { pageToken: string }).pageToken;
          return {};
        },
      }),
    );
    const res = await client.listItems({});
    expect(seenToken).toBe("");
    expect(res.items).toEqual([]);
    expect(res.nextPageToken).toBe("");
  });
});
`
