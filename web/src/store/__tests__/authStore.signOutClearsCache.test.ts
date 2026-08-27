import { beforeEach, describe, expect, it, vi } from "vitest";

// ---------------------------------------------------------------------------
// Signing out must drop every cached answer about WHO the user is.
//
// The QueryClient is a module-level singleton (lib/query-client.ts) shared by
// the whole app, and the onboarding queries are keyed on ['onboarding', …]
// with NO user id in the key. `signOut` reset seven Zustand stores and left
// that cache untouched, so the next user inherited the previous user's
// answers for a full staleTime window:
//
//   ['onboarding','currentUser'] → { onboardingCompleted: true }
//   ['onboarding','daemons']     → the previous user's daemons
//
// Observed on dev: sign out, sign in anonymously, and onboarding never
// appears. ModernApp gates on `currentUser.onboardingCompleted`, reads the
// PREVIOUS user's `true` from cache, and sends the brand-new account straight
// into the app. The control-plane log shows the new anonymous user
// (email="") issuing CompleteOnboarding 91ms after load with no interaction,
// while their daemon did not exist until 5s later — the client was acting on
// the departed user's state, not on anything the server said about this one.
//
// `cloud.getCurrentUser` holds a second, independent 30s promise cache for the
// same reason, so signing out has to clear that too.
// ---------------------------------------------------------------------------

const resetMock = vi.fn();
const zustandStore = { getState: () => ({ reset: resetMock }) };

vi.mock("../chatStore", () => ({ useChatStore: zustandStore }));
vi.mock("../projectStore", () => ({ useProjectStore: zustandStore }));
vi.mock("../worktreeStore", () => ({ useWorktreeStore: zustandStore }));
vi.mock("../chatNavigationStore", () => ({ useChatNavigationStore: zustandStore }));
vi.mock("../attachmentStore", () => ({ useAttachmentStore: zustandStore }));
vi.mock("../tasksStore", () => ({ useTasksStore: zustandStore }));
vi.mock("../processStore", () => ({ useProcessStore: zustandStore }));

vi.mock("@/lib/supabase", () => ({
  supabase: { auth: { signOut: vi.fn().mockResolvedValue({ error: null }) } },
}));

import { queryClient } from "@/lib/query-client";
import { useAuthStore } from "../authStore";

beforeEach(() => {
  resetMock.mockClear();
  queryClient.clear();
});

describe("signOut and the shared query cache", () => {
  it("does not leave the previous user's onboarding answers cached", async () => {
    // The departed user: onboarding finished, one daemon registered.
    queryClient.setQueryData(["onboarding", "currentUser"], {
      onboardingCompleted: true,
      id: "user-a",
    });
    queryClient.setQueryData(["onboarding", "daemons"], [{ id: "daemon-a" }]);

    await useAuthStore.getState().signOut();

    // Whatever the next user gets, it must not be user-a's answers. Reading
    // `onboardingCompleted: true` here is exactly what skips onboarding for a
    // brand-new account.
    expect(queryClient.getQueryData(["onboarding", "currentUser"])).toBeUndefined();
    expect(queryClient.getQueryData(["onboarding", "daemons"])).toBeUndefined();
  });

  it("still resets the zustand stores it always reset", async () => {
    // Guard against "fixing" the cache by reordering signOut and dropping the
    // store resets on an early return.
    await useAuthStore.getState().signOut();
    expect(resetMock).toHaveBeenCalled();
  });
});
