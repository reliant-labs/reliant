import { useCallback, useEffect, useState, type ComponentProps } from "react";

import { useComposerHandoffStore, type ComposerHandoff } from "../../store/composerHandoffStore";
import { useProjectStore } from "../../store/projectStore";
import { NewChatView } from "./NewChatView";

/**
 * The app's own new-chat screen: NewChatView, plus whatever another screen left
 * for it in the composer handoff store (composerHandoffStore.ts).
 *
 * The handoff is taken into state on the way in, so a later remount of this
 * view starts empty — the text was offered once, to the screen the user landed
 * on, and is not re-applied the next time they open a new chat.
 */
export function MainNewChatView(props: ComponentProps<typeof NewChatView>) {
  const { onChatCreated: hostOnChatCreated, composerPrefill: hostPrefill } = props;
  const projectId = useProjectStore((state) => state.currentProject?.id);
  const pending = useComposerHandoffStore((state) => state.pending);
  const [handoff, setHandoff] = useState<ComposerHandoff | null>(null);

  useEffect(() => {
    if (!pending || !projectId) return;
    const taken = useComposerHandoffStore.getState().take(projectId);
    if (taken) setHandoff(taken);
  }, [pending, projectId]);

  const onChatCreated = useCallback(
    (chatId: string) => {
      handoff?.onChatCreated?.(chatId);
      setHandoff(null);
      hostOnChatCreated?.(chatId);
    },
    [handoff, hostOnChatCreated]
  );

  return (
    <NewChatView
      {...props}
      composerPrefill={handoff ? { text: handoff.text, id: handoff.id } : hostPrefill}
      onChatCreated={onChatCreated}
    />
  );
}
