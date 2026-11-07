import { StrictMode } from "react";
import { render, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { createClient, type Transport } from "@connectrpc/connect";
import { afterEach, expect, it, vi } from "vitest";
import { PreviewSession } from "./SetupSurfacesPreview";
import { setTransport, transport } from "@/lib/connect";
import { useAuthStore } from "@/store/authStore";
import { ProviderAccountService } from "@/gen/reliant/v1/provider_account_pb";

const original = useAuthStore.getState().session;
afterEach(() => { setTransport(null); useAuthStore.setState({ session: original }); });

it("restores the previous RPC transport and session when leaving the preview (also under StrictMode)", async () => {
  const real = vi.fn<Transport["unary"]>(async (method) => ({
    service: method.parent, method, stream: false, header: new Headers(), trailer: new Headers(),
    message: create(method.output, {} as never),
  }) as never);
  setTransport({ unary: real, stream: vi.fn() } as Transport);
  const session = { access_token: "original-session" } as NonNullable<typeof original>;
  useAuthStore.setState({ session });
  const api = createClient(ProviderAccountService, transport);
  const mounted = render(<StrictMode><PreviewSession fixtures={{}} signedIn><p>Preview</p></PreviewSession></StrictMode>);
  await waitFor(() => expect(useAuthStore.getState().session).not.toBe(session));
  await api.listProviderAccounts({});
  expect(real).not.toHaveBeenCalled();
  mounted.unmount();
  expect(useAuthStore.getState().session).toBe(session);
  await api.listProviderAccounts({});
  expect(real).toHaveBeenCalledTimes(1);
});
