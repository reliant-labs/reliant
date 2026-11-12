// Fixture: a store that builds its own Connect client (through the app's
// connect seam) and fetches into Zustand. No generated import is needed to
// hold server data this way.

import { create } from "zustand";
import { connectClient } from "@/lib/connect";
import { services } from "@/lib/services";

const client = connectClient(services.orders);

type OrdersState = {
  orders: unknown[];
  load: () => Promise<void>;
};

export const useOrdersStore = create<OrdersState>((set) => ({
  orders: [],
  load: async () => {
    const response = await client.listOrders({});
    set({ orders: response.orders });
  },
}));
