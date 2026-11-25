// Fixture: a client-state store that uses a generated ENUM value (and a
// message type). Neither is a client: nothing here can reach a server.

import { create } from "zustand";
import { ChatState, type ChatSelection } from "../gen/reliant/v1/chat_pb";

type NavigationState = {
  state: ChatState;
  selection: ChatSelection | null;
};

export const useNavigationStore = create<NavigationState>(() => ({
  state: ChatState.IDLE,
  selection: null,
}));
