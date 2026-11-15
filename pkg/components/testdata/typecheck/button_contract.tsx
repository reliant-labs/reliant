// The Button's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every `@ts-expect-error`
// below is a misuse the type must REJECT: if the type loosens, the directive
// itself becomes the error ("Unused '@ts-expect-error' directive").
import React from "react";

import Button, { type ButtonSize, type ButtonVariant } from "./button";
import { buttonClasses } from "./button_classes";

const Icon = () => <svg aria-hidden="true" />;

const variants: ButtonVariant[] = ["primary", "secondary", "outline", "ghost", "danger"];
const sizes: ButtonSize[] = ["xs", "sm", "md", "lg", "xl"];

export function Accepted() {
  const ref = React.useRef<HTMLButtonElement>(null);
  return (
    <>
      {variants.map((v) =>
        sizes.map((s) => (
          <Button key={`${v}-${s}`} variant={v} size={s}>
            Save
          </Button>
        )),
      )}
      <Button ref={ref} type="submit" loading fullWidth onClick={(e) => e.currentTarget.blur()}>
        Submit
      </Button>
      <Button leftIcon={<Icon />} rightIcon={<Icon />}>
        Both icons
      </Button>
      <Button icon={<Icon />} aria-label="Close" variant="ghost" size="sm" />
      <Button href="/settings" variant="outline">
        Settings
      </Button>
      <Button href="https://example.com" target="_blank" rel="noreferrer" disabled>
        External
      </Button>
      <label className={buttonClasses({ variant: "secondary", size: "sm" })}>
        Upload
        <input type="file" className="sr-only" />
      </label>
      <span className={buttonClasses({ iconOnly: true, size: "xs" }, "ms-2")} />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error — an icon-only button must carry an aria-label */}
      <Button icon={<Icon />} />
      {/* @ts-expect-error — icon-only takes no children */}
      <Button icon={<Icon />} aria-label="Close">
        Close
      </Button>
      {/* @ts-expect-error — the loading prop is `loading`, not `isLoading` */}
      <Button isLoading>Save</Button>
      {/* @ts-expect-error — the destructive variant is `danger` */}
      <Button variant="destructive">Delete</Button>
      {/* @ts-expect-error — `default` is not a variant; primary is the default */}
      <Button variant="default">Save</Button>
      {/* @ts-expect-error — sizes are xs..xl */}
      <Button size="icon">Save</Button>
      {/* @ts-expect-error — a link button takes anchor attributes, not button ones */}
      <Button href="/x" form="f">
        Go
      </Button>
    </>
  );
}
