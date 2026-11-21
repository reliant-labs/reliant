/**
 * Vertex AI in the "Add an API key" dialog: a multi-line field for the
 * service-account key file, checked for shape before anything is sent, and
 * sent whole when it looks right.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ validate: vi.fn(), add: vi.fn() }));

vi.mock("../../hooks/useOAuthAvailability", () => ({
  useOAuthAvailability: () => ({ available: true, loading: false, recheck: vi.fn(), problem: null, stopped: false }),
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock(import("../../hooks/settings-service-hooks_gen"), async (importOriginal) => ({
  ...(await importOriginal()),
  useValidateProviderAPIKey: (() => ({ mutateAsync: mocks.validate })) as never,
}));
vi.mock(import("../../hooks/provider-account-queries"), async (importOriginal) => ({
  ...(await importOriginal()),
  addProviderApiKey: mocks.add,
}));
vi.mock("../../store/globalDataStore", () => ({
  useGlobalDataStore: { getState: () => ({ refetchModels: vi.fn() }) },
}));

import { ApiKeySetupModal } from "../ApiKeySetupModal";

const KEY = JSON.stringify(
  {
    type: "service_account",
    project_id: "acme-prod",
    private_key: "fake-private-key-material",
    client_email: "reliant@acme-prod.iam.gserviceaccount.com",
  },
  null,
  2,
);

function renderModal() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ApiKeySetupModal isOpen onClose={vi.fn()} initialProvider="vertexai" />
    </QueryClientProvider>,
  );
  return screen.getByLabelText("Service-account key (JSON)");
}

beforeEach(() => {
  mocks.validate.mockReset();
  mocks.add.mockReset();
});

describe("ApiKeySetupModal — Vertex AI", () => {
  it("takes the key file in a multi-line field, with the steps to make one", () => {
    const field = renderModal();
    expect(field.tagName).toBe("TEXTAREA");
    expect(screen.queryByPlaceholderText("sk-ant-...")).not.toBeInTheDocument();
    expect(screen.getByText(/roles\/aiplatform\.user/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /IAM › Service accounts/ })).toHaveAttribute(
      "href",
      "https://console.cloud.google.com/iam-admin/serviceaccounts",
    );
  });

  it("names a paste that is not a service-account key, and sends nothing", async () => {
    const field = renderModal();
    await userEvent.click(field);
    await userEvent.paste(JSON.stringify({ type: "authorized_user", client_id: "x" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByTestId("service-account-key-error")).toHaveTextContent(/gcloud user login/);
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(mocks.validate).not.toHaveBeenCalled();
    expect(mocks.add).not.toHaveBeenCalled();
  });

  it("sends the whole key file as the provider's key", async () => {
    mocks.validate.mockResolvedValue({ valid: true, message: "" });
    mocks.add.mockResolvedValue({ created: true });
    const field = renderModal();
    await userEvent.click(field);
    await userEvent.paste(KEY);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.add).toHaveBeenCalledWith("vertexai", KEY));
    expect(mocks.validate).toHaveBeenCalledWith({ provider: "vertexai", apiKey: KEY });
    expect(screen.queryByTestId("service-account-key-error")).not.toBeInTheDocument();
  });
});
