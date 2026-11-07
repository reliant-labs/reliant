import { useState } from "react";
import { pasteCounts, pastedFilename, type useLongPaste } from "./useLongPaste";
import Button from "../forge-ui/button";

export function LongPastePreview({ paste, disabled = false }: { paste: ReturnType<typeof useLongPaste>; disabled?: boolean }) {
  const [editing, setEditing] = useState<number | null>(null);
  const [text, setText] = useState("");
  return <fieldset disabled={disabled} className="min-w-0 space-y-2">
    {paste.notice && <p role="status" className="text-xs text-muted-foreground">{paste.notice}</p>}
    {paste.rows.map((row) => {
      const counts = pasteCounts(row.text);
      return <details key={row.id} className="rounded-md border border-border bg-background p-2">
        <summary className="cursor-pointer text-sm">{pastedFilename(row.id)} · {counts.characters} characters · {counts.lines} lines</summary>
        {editing === row.id ? <textarea aria-label="Edit pasted text" className="mt-2 w-full rounded border border-border bg-background p-2 text-foreground" value={text} onChange={(e) => { if (paste.edit(row.id, e.target.value)) setText(e.target.value); }} /> : <pre className="max-h-48 overflow-auto whitespace-pre-wrap text-xs">{row.text}</pre>}
        <div className="forge-ui flex flex-wrap gap-2">
          {editing === row.id ? <Button className="touch:min-h-11" variant="outline" onClick={() => { if (paste.edit(row.id, text)) setEditing(null); }}>Save edit</Button> : <Button className="touch:min-h-11" variant="outline" onClick={() => { setEditing(row.id); setText(row.text); }}>Edit</Button>}
          <Button className="touch:min-h-11" variant="outline" onClick={() => paste.insert(row.id)}>Insert as text</Button>
          {(paste.status(row.id) === "restored" || paste.status(row.id) === "failed") && <Button className="touch:min-h-11" variant="outline" onClick={() => paste.retry(row.id)}>{row.upload === undefined ? "Upload attachment" : "Retry upload"}</Button>}
          <Button className="touch:min-h-11" variant="ghost" onClick={() => paste.remove(row.id)}>Remove</Button>
        </div>
        {paste.status(row.id) === "uploading" && <p role="status" className="text-xs text-muted-foreground">Uploading…</p>}
        {paste.status(row.id) === "failed" && <p role="alert" className="text-xs text-destructive-ink">Upload failed. Text is retained; retry or insert as text.</p>}
        {row.upload === undefined && <p className="text-xs text-muted-foreground">Draft restored or edited. Upload it again or insert as text before sending.</p>}
      </details>;
    })}
  </fieldset>;
}
