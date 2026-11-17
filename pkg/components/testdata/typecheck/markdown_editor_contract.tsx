// The MarkdownEditor's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import MarkdownEditor, { type MarkdownCharLimit, type MarkdownEditorTab } from "./markdown_editor";

const limits: MarkdownCharLimit[] = [
  { label: "Bluesky", max: 300 },
  { label: "X", max: 280 },
];

export function Accepted() {
  const [draft, setDraft] = React.useState("");
  const [tab, setTab] = React.useState<MarkdownEditorTab>("write");
  return (
    <>
      <MarkdownEditor label="Post" value={draft} onChange={setDraft} limits={limits} />
      <MarkdownEditor
        ariaLabel="Notes"
        defaultValue="# Hi"
        rows={4}
        disabled
        invalid
        name="notes"
      />
      <MarkdownEditor
        label={<span>Changelog</span>}
        tab={tab}
        onTabChange={setTab}
        measure={(v) => v.replace(/https?:\/\/\S+/g, "x".repeat(23)).length}
        renderPreview={(md) => <pre>{md}</pre>}
      />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error a limit needs a numeric max */}
      <MarkdownEditor limits={[{ label: "Bluesky", max: "300" }]} />
      {/* @ts-expect-error onChange receives the string, not the event */}
      <MarkdownEditor onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => e} />
      {/* @ts-expect-error there is no "split" tab */}
      <MarkdownEditor defaultTab="split" />
    </>
  );
}
