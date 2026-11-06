// TEMPORARY (README.md): where a Reliant tour ends, as the tour's navigation
// sees it in the demo. The forge journey ends on the console (its `finish`),
// so this is never taken there.

import type { chatRouteAfterTour as Real } from "@/components/OnboardingFlow/useOnboardingComplete";

export const chatRouteAfterTour: typeof Real = () => ({ to: "/", search: { tour: undefined } });
