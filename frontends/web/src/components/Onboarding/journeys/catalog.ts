/**
 * The tour's Reliant-side wiring, for the shells that carry Reliant code
 * (routes.tsx, forgeShellRoutes.tsx, OnboardingWizard). Imported for its
 * effects:
 *
 *  - registers the workflows journey's content (registry.ts keeps only a
 *    placeholder for it, so the forge demo's bundle needn't carry it);
 *  - installs what a tour's end does in Reliant: land on the chat, and ask
 *    for an API key (tourShell.ts `setReliantTourHooks`).
 *
 * The forge demo's bundle (demo.html) never imports this module.
 */

import { useChatStore } from "../../../store/chatStore";
import { useProjectStore } from "../../../store/projectStore";
import { chatRouteAfterTour } from "../../OnboardingFlow/useOnboardingComplete";
import { setReliantTourHooks } from "../tourShell";
import { registerJourney } from "./registry";
import { workflowsJourney } from "./workflows";

registerJourney(workflowsJourney);

setReliantTourHooks({
  landOnChat: async (navigate) => {
    useChatStore.getState().clearCurrentChat();
    const projectId = useProjectStore.getState().currentProject?.id;
    await navigate(chatRouteAfterTour(projectId) as never);
  },
  afterTour: async () => {
    const { useApiKeySetupStore } = await import("../../../store/apiKeySetupStore");
    if (useApiKeySetupStore.getState().hasApiKey === true) return;
    useApiKeySetupStore.setState({ hasChecked: false });
    await useApiKeySetupStore.getState().ensureApiKeyOrShowModal();
  },
});
