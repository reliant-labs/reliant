// Fixture: a client-state store that names a generated message TYPE (the
// selection it holds is shaped like one) but never reaches a server. A
// type-only import is erased at compile time, so it is not the smell.

import { create } from "zustand";
import type { ChatSelection } from "../gen/reliant/v1/chat_pb";

type SelectionState = {
  selection: ChatSelection | null;
};

export const useSelectionStore = create<SelectionState>(() => ({
  selection: null,
}));
