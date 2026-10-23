// Fixture: pre-workspaces single-frontend layout (web/src/store/, singular).
// Demonstrates the analyzer also scans the historic shape so projects
// that haven't migrated to the workspaces layout still get the warning.

import { create } from "zustand";
import { OrdersServiceClient } from "../../gen/orders/v1/orders_connect";

type OrdersState = { count: number };

export const useOrdersStore = create<OrdersState>(() => ({
  count: 0,
}));

export type _ClientRef = OrdersServiceClient;
