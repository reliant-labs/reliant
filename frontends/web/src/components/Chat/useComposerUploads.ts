import { useEffect, useRef, useState } from "react";

type Upload = { id: number; file: File; session: string; status: "uploading" | "failed" };
type Attach = (session: string, file: File) => Promise<{ id: string }>;
type Remove = (session: string, id: string) => void;
/** Session switches retain each draft's work, but only expose the active draft.
 * Unmount cancels unfinished work; already completed store attachments survive.
 * Each operation owns its matching attach/remove pair, even across rerenders. */
export function useComposerUploads(attach: Attach, remove: Remove, session: string) {
  const [uploads, setUploads] = useState<Upload[]>([]);
  const pending = useRef(new Map<number, { file: File; session: string; attach: Attach; remove: Remove }>());
  const activeSession = useRef(session);
  activeSession.current = session;
  const generated = useRef(new Set<number>());
  const completed = useRef(new Map<number, { session: string; id: string; remove: Remove; sendOwned?: boolean }>());
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    const entries = pending.current;
    return () => {
      mounted.current = false;
      entries.clear();
      completed.current.forEach((entry) => { if (!entry.sendOwned) entry.remove(entry.session, entry.id); });
      completed.current.clear();
    };
  }, []);
  const nextId = useRef(0);
  const inFlight = useRef(new Set<number>());
  const run = async (id: number) => {
    const entry = pending.current.get(id);
    if (!entry || inFlight.current.has(id)) return;
    inFlight.current.add(id);
    setUploads((rows) => rows.map((row) => row.id === id ? { ...row, status: "uploading" } : row));
    try {
      const attachment = await entry.attach(entry.session, entry.file);
      if (!pending.current.has(id)) entry.remove(entry.session, attachment.id);
      else {
        if (generated.current.has(id)) completed.current.set(id, { session: entry.session, id: attachment.id, remove: entry.remove });
        pending.current.delete(id);
        setUploads((rows) => rows.filter((row) => row.id !== id));
      }
    } catch {
      if (pending.current.has(id)) setUploads((rows) => rows.map((row) => row.id === id ? { ...row, status: "failed" } : row));
    } finally {
      inFlight.current.delete(id);
    }
  };
  return {
    uploads: uploads.filter((upload) => upload.session === session),
    // Synchronous ownership handle for reversible generated attachments.
    enqueue: (file: File) => {
      const id = ++nextId.current;
      generated.current.add(id);
      pending.current.set(id, { file, session, attach, remove });
      setUploads((rows) => [...rows, { id, file, session, status: "uploading" }]);
      void run(id);
      return id;
    },
    discard: (id: number) => {
      pending.current.delete(id);
      setUploads((rows) => rows.filter((row) => row.id !== id));
      const done = completed.current.get(id);
      if (done && !done.sendOwned) done.remove(done.session, done.id);
      completed.current.delete(id);
    },
    attachmentId: (id: number) => completed.current.get(id)?.id,
    // Keep identity for rendering while deletion belongs to the send lifecycle.
    transferToSend: (id: number) => {
      const entry = completed.current.get(id);
      if (entry) entry.sendOwned = true;
    },
    isSendOwned: (attachmentId: string) => Array.from(completed.current.values()).some((entry) => entry.id === attachmentId && entry.sendOwned),
    forgetCompleted: (id: number) => { completed.current.delete(id); },
    hasPending: () => Array.from(pending.current.values()).some((entry) => entry.session === activeSession.current),
    add: async (targetSession: string, files: File[]) => {
      if (!mounted.current || targetSession !== activeSession.current) return;
      const additions = files.map((file) => {
        const id = ++nextId.current;
        pending.current.set(id, { file, session: targetSession, attach, remove });
        return { id, file, session: targetSession, status: "uploading" as const };
      });
      setUploads((rows) => [...rows, ...additions]);
      for (const { id } of additions) await run(id);
    },
    retry: async (id: number) => {
      if (!mounted.current || pending.current.get(id)?.session !== activeSession.current) return;
      await run(id);
    },
    cancel: (id: number) => {
      if (!mounted.current || pending.current.get(id)?.session !== activeSession.current) return;
      pending.current.delete(id);
      setUploads((rows) => rows.filter((row) => row.id !== id));
    },
  };
}
