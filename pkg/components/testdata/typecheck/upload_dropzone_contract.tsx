// The UploadDropzone's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import UploadDropzone, { type UploadEntry } from "./upload_dropzone";

async function sign(file: File) {
  return {
    url: `https://bucket.example/${file.name}`,
    headers: { "Content-Type": file.type },
    key: file.name,
  };
}

export function Accepted() {
  const [busy, setBusy] = React.useState(false);
  return (
    <>
      <UploadDropzone getUploadUrl={async () => ({ url: "https://bucket.example/x" })} />
      <UploadDropzone
        getUploadUrl={sign}
        // The extra fields getUploadUrl returned come back typed.
        onUploaded={(file, target) => void [file.name, target.key.toUpperCase()]}
        onError={(file, error) => void [file.name, error.message]}
        onEntriesChange={(entries: UploadEntry[]) =>
          setBusy(entries.some((e) => e.status === "uploading" || e.status === "queued"))
        }
        accept="image/*,.pdf"
        maxBytes={10 * 1024 * 1024}
        multiple={false}
        maxConcurrent={2}
        disabled={busy}
        label="Drop a screenshot"
        hint={<span>PNG or JPG</span>}
      />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error getUploadUrl returns a promise of a target, not a bare URL */}
      <UploadDropzone getUploadUrl={async () => "https://bucket.example/x"} />
      {/* @ts-expect-error the target needs a url */}
      <UploadDropzone getUploadUrl={async () => ({ headers: {} })} />
      <UploadDropzone
        getUploadUrl={async () => ({ url: "https://bucket.example/x" })}
        // @ts-expect-error a target without `key` does not grow one
        onUploaded={(_file, target) => void target.key}
      />
      {/* @ts-expect-error maxBytes is a number of bytes */}
      <UploadDropzone getUploadUrl={sign} maxBytes="10MB" />
    </>
  );
}
