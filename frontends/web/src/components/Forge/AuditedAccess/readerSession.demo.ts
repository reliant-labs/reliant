// Demo-only twin: no authStore/Supabase import, credentials or real reader.
import type * as Real from "./readerSession";
const reader: Real.ReaderSession = { principalId: "fixture-reader", epoch: "demo-fixture" };
export const getReaderSession: typeof Real.getReaderSession = () => reader;
export const useReaderSession: typeof Real.useReaderSession = () => reader;
export const isCurrentReader: typeof Real.isCurrentReader = (candidate) => candidate === reader;
export const setReaderPrincipal: typeof Real.setReaderPrincipal = () => undefined;
export const subscribeReaderSession: typeof Real.subscribeReaderSession = () => () => undefined;
export const requireCurrentReader: typeof Real.requireCurrentReader = (candidate) => {
  if (candidate !== reader) throw new DOMException("Reader session changed", "AbortError");
};
export const readerStorageScope: typeof Real.readerStorageScope = () => "fixture-reader.demo-fixture";
export type { ReaderSession } from "./readerSession";
