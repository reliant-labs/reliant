# Interim substitutes (TEMPORARY)

The forge demo's bundle (`vite build --mode demo`) must not contain Reliant-only
modules (`vite-plugin-demo-bundle.ts`). A few tour-engine files still import
some; their owners are cutting those imports. Until then the demo build
substitutes these files for those imports ONLY (`INTERIM_SUBSTITUTES` in
`vite-plugin-demo-bundle.ts`, keyed by importer), so the same module imported
from anywhere else still fails the guard.

| Importer | Imports | Substitute |
|---|---|---|
| `components/Onboarding/journeys/registry.ts` | `./workflows` (Reliant's workflows journey) | `workflowsJourney.ts` |
| `components/Onboarding/useTourNavigation.ts` | `store/chatStore` | `chatStore.ts` |
| `components/Onboarding/useTourNavigation.ts` | `OnboardingFlow/useOnboardingComplete` | `useOnboardingComplete.ts` |

Delete a row, its file and its `INTERIM_SUBSTITUTES` entry once the import is
gone; delete this directory with the last one.
