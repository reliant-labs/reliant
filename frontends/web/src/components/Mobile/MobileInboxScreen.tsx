/**
 * `/m/inbox` — everything waiting on the user, on a phone.
 *
 * Not a mobile re-implementation: it is the desktop Inbox's own view
 * (`inbox/InboxPage.tsx` → `InboxView`) inside the mobile chrome, so the
 * data, the sections, dismiss + Undo, approve/deny, waking a machine and
 * answering a question (async ones included, through the same
 * `QuestionPrompt` + `answerQuestion` the desktop row uses) are one code path.
 * What the phone changes is presentation: the screen header carries the
 * title and the menu button, the rows grow to touch size on a coarse pointer,
 * and run links open the run as a chat (see `inbox/InboxItem.tsx`).
 *
 * The update stream `MobileShell` connects keeps it live, exactly as the
 * desktop Inbox is: the same user updates invalidate the same queries.
 */

import { InboxView } from "../inbox/InboxPage";
import ErrorBoundary from "../ErrorBoundary";
import { MobileMenuButton } from "./MobileMenuButton";
import { MobileScreenBody, MobileScreenHeader } from "./MobileChrome";

export function MobileInboxScreen() {
  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="mobile-inbox-screen">
      <MobileScreenHeader title="Inbox" leading={<MobileMenuButton />} />
      <MobileScreenBody>
        <ErrorBoundary layout="panel">
          <InboxView titled={false} />
        </ErrorBoundary>
      </MobileScreenBody>
    </div>
  );
}
