// The Combobox's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import Combobox, { type ComboboxOption } from "./combobox";

const options: ComboboxOption[] = [
  { value: "a", label: "Alpha", description: "First", group: "Letters", keywords: ["first"] },
  { value: "b", label: "Beta", icon: <svg aria-hidden="true" />, disabled: true },
];

export function Accepted() {
  const [one, setOne] = React.useState<string | null>(null);
  const [many, setMany] = React.useState<string[]>([]);
  return (
    <>
      <Combobox
        options={options}
        value={one}
        onChange={setOne}
        clearable
        size="sm"
        aria-label="One"
      />
      <Combobox
        multiple
        options={options}
        value={many}
        onChange={setMany}
        loading
        invalid
        aria-label="Many"
      />
      <Combobox
        options={options}
        value={one}
        onChange={setOne}
        aria-label="Chip"
        trigger={({ selected, open, ...props }) => (
          <button type="button" {...props} data-open={open}>
            {selected[0]?.label ?? "None"}
          </button>
        )}
      />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error a single combobox's value is one string or null */}
      <Combobox options={options} value={["a"]} onChange={() => undefined} />
      {/* @ts-expect-error a multiple combobox's value is a list */}
      <Combobox multiple options={options} value="a" onChange={() => undefined} />
      {/* @ts-expect-error a multiple combobox has no clear button: its footer clears */}
      <Combobox multiple clearable options={options} value={[]} onChange={() => undefined} />
      {/* @ts-expect-error size is sm, md or lg */}
      <Combobox options={options} value={null} onChange={() => undefined} size="xl" />
    </>
  );
}
