import { useQuery } from "@tanstack/react-query";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { EntityPicker } from "@/components/entity-picker";
import { mockTransport, renderWithTransport } from "@/lib/test-utils";

import type { ConnectClientError } from "@reliantlabs/forge-web-runtime";
import type { UseQueryOptions } from "@tanstack/react-query";

// Stand-in for a generated list hook: same `(request, options?)` signature
// `forge generate` emits, same ConnectClientError the transport's
// error-normalize interceptor throws, same paged/filtered response shape.
// The error type is part of the signature being stood in for: typed as a bare
// `Error` this stand-in stops satisfying EntityListHook, and the test would be
// exercising a shape `forge generate` never emits.
interface Patient {
  id: string;
  fullName: string;
}
interface ListPatientsRequest {
  pageSize?: number;
  search?: string;
}
interface ListPatientsResponse {
  patients: Patient[];
  nextPageToken: string;
}

const roster: Patient[] = [
  { id: "p_1", fullName: "Ada Lovelace" },
  { id: "p_2", fullName: "Grace Hopper" },
];

function useListPatients(
  req: ListPatientsRequest,
  options?: Partial<UseQueryOptions<ListPatientsResponse, ConnectClientError>>,
) {
  return useQuery<ListPatientsResponse, ConnectClientError>({
    queryKey: ["patients", req.search ?? ""],
    // The SERVER does the filtering — mirrored here so the assertions prove
    // the search text reached the request, not a client-side array filter.
    queryFn: () => {
      const term = (req.search ?? "").toLowerCase();
      return Promise.resolve({
        patients:
          term === ""
            ? roster
            : roster.filter((p) => p.fullName.toLowerCase().includes(term)),
        nextPageToken: "",
      });
    },
    ...options,
  });
}

function Harness({ onPick }: { onPick: (id: string | undefined) => void }) {
  const [value, setValue] = useState<string | undefined>(undefined);
  return (
    <EntityPicker
      useList={useListPatients}
      buildRequest={(search) => ({ pageSize: 20, search: search || undefined })}
      itemsOf={(res) => res.patients}
      hasMoreOf={(res) => Boolean(res.nextPageToken)}
      optionValue={(p) => p.id}
      optionLabel={(p) => p.fullName}
      value={value}
      onChange={(id) => {
        setValue(id);
        onPick(id);
      }}
      debounceMs={0}
      aria-label="Patient"
      placeholder="Select a patient"
    />
  );
}

afterEach(cleanup);

function open() {
  fireEvent.click(screen.getByRole("button", { name: "Patient" }));
}

describe("EntityPicker", () => {
  it("lists rows from the list hook once opened", async () => {
    renderWithTransport(<Harness onPick={() => {}} />, {
      transport: mockTransport(),
    });
    open();
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "Ada Lovelace" })).toBeTruthy(),
    );
    expect(screen.getByRole("option", { name: "Grace Hopper" })).toBeTruthy();
  });

  it("pushes the search text into the list REQUEST, not a client-side filter", async () => {
    renderWithTransport(<Harness onPick={() => {}} />, {
      transport: mockTransport(),
    });
    open();
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "Ada Lovelace" })).toBeTruthy(),
    );

    fireEvent.change(screen.getByRole("combobox"), {
      target: { value: "grace" },
    });
    await waitFor(() =>
      expect(screen.queryByRole("option", { name: "Ada Lovelace" })).toBeNull(),
    );
    expect(screen.getByRole("option", { name: "Grace Hopper" })).toBeTruthy();
  });

  it("selects with the keyboard and reports the id", async () => {
    const picked: (string | undefined)[] = [];
    renderWithTransport(<Harness onPick={(id) => picked.push(id)} />, {
      transport: mockTransport(),
    });
    open();
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "Ada Lovelace" })).toBeTruthy(),
    );

    const search = screen.getByRole("combobox");
    fireEvent.keyDown(search, { key: "ArrowDown" });
    fireEvent.keyDown(search, { key: "Enter" });

    expect(picked).toEqual(["p_2"]);
    // Closed, and showing the picked row's NAME rather than its id.
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Patient" }).textContent,
    ).toContain("Grace Hopper");
  });

  // A List RPC with no free-text filter has nowhere to put the search text,
  // so the picker must not ship a search box that silently does nothing.
  // `forge scaffold page` emits searchable={false} for exactly that case.
  it("hides the search box when searchable is false and still navigates by keyboard", async () => {
    const picked: (string | undefined)[] = [];
    renderWithTransport(
      <EntityPicker
        useList={useListPatients}
        buildRequest={() => ({ pageSize: 20 })}
        itemsOf={(res) => res.patients}
        optionValue={(p) => p.id}
        optionLabel={(p) => p.fullName}
        value={undefined}
        onChange={(id) => picked.push(id)}
        searchable={false}
        aria-label="Patient"
        placeholder="Select a patient"
      />,
      { transport: mockTransport() },
    );
    open();
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "Ada Lovelace" })).toBeTruthy(),
    );
    expect(screen.queryByRole("combobox")).toBeNull();

    const listbox = screen.getByRole("listbox");
    fireEvent.keyDown(listbox, { key: "ArrowDown" });
    fireEvent.keyDown(listbox, { key: "Enter" });
    expect(picked).toEqual(["p_2"]);
  });

  it("clears the selection", async () => {
    const picked: (string | undefined)[] = [];
    renderWithTransport(<Harness onPick={(id) => picked.push(id)} />, {
      transport: mockTransport(),
    });
    open();
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "Ada Lovelace" })).toBeTruthy(),
    );
    fireEvent.click(screen.getByRole("option", { name: "Ada Lovelace" }));

    fireEvent.click(screen.getByRole("button", { name: "Clear selection" }));
    expect(picked).toEqual(["p_1", undefined]);
  });
});
