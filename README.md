# Restyle v1 gallery

Every surface of the visual lane (`frontends/web/e2e/visual`), captured in the pinned Playwright image at 1440×900 (desktop dark and light) and 390×844 (mobile dark), against the same fixture data.

- `before-*`: what `main` renders today.
- `after-A-inter-*`: the restyle with **Inter** as the UI face (Geist Mono for code).
- `after-B-geist-*`: the restyle with **Geist** as the UI face (today's font; Geist Mono for code). This is what `feat/web-restyle-v1` commits.
- `pr1-*`, `pr2-*`, `pr3-*`: each stacked PR's own state (tokens and primitives; + chat; + dashboards). `pr3` is the whole restyle with Geist.

Name pattern: `<variant>-<surface>-<project>.png`. The font is one line: `--font-ui-face` in `frontends/web/src/index.css`.

## Font: Geist (owner's decision, 2026-11-19)

The owner compared A (Inter) with B (Geist) and chose **Geist**, which is what every branch ships. I had recommended Inter as the softer read at 13–14px. The face is one line, `--font-ui-face` in `frontends/web/src/index.css`.

## Surfaces

| Surface | Projects | before | after A (Inter) | after B (Geist) |
|---|---|---|---|---|
| app-overview | desktop-dark | `before-app-overview-desktop-dark.png` | `after-A-inter-app-overview-desktop-dark.png` | `after-B-geist-app-overview-desktop-dark.png` |
| app-overview | desktop-light | `before-app-overview-desktop-light.png` | `after-A-inter-app-overview-desktop-light.png` | `after-B-geist-app-overview-desktop-light.png` |
| app-overview | mobile-dark | `before-app-overview-mobile-dark.png` | `after-A-inter-app-overview-mobile-dark.png` | `after-B-geist-app-overview-mobile-dark.png` |
| billing | desktop-dark | `before-billing-desktop-dark.png` | `after-A-inter-billing-desktop-dark.png` | `after-B-geist-billing-desktop-dark.png` |
| billing | desktop-light | `before-billing-desktop-light.png` | `after-A-inter-billing-desktop-light.png` | `after-B-geist-billing-desktop-light.png` |
| chat-approval | desktop-dark | `before-chat-approval-desktop-dark.png` | `after-A-inter-chat-approval-desktop-dark.png` | `after-B-geist-chat-approval-desktop-dark.png` |
| chat-approval | desktop-light | `before-chat-approval-desktop-light.png` | `after-A-inter-chat-approval-desktop-light.png` | `after-B-geist-chat-approval-desktop-light.png` |
| chat-approval | mobile-dark | `before-chat-approval-mobile-dark.png` | `after-A-inter-chat-approval-mobile-dark.png` | `after-B-geist-chat-approval-mobile-dark.png` |
| chat | desktop-dark | `before-chat-desktop-dark.png` | `after-A-inter-chat-desktop-dark.png` | `after-B-geist-chat-desktop-dark.png` |
| chat | desktop-light | `before-chat-desktop-light.png` | `after-A-inter-chat-desktop-light.png` | `after-B-geist-chat-desktop-light.png` |
| chat | mobile-dark | `before-chat-mobile-dark.png` | `after-A-inter-chat-mobile-dark.png` | `after-B-geist-chat-mobile-dark.png` |
| chat-detailed | desktop-dark | `before-chat-detailed-desktop-dark.png` | `after-A-inter-chat-detailed-desktop-dark.png` | `after-B-geist-chat-detailed-desktop-dark.png` |
| chat-detailed | desktop-light | `before-chat-detailed-desktop-light.png` | `after-A-inter-chat-detailed-desktop-light.png` | `after-B-geist-chat-detailed-desktop-light.png` |
| chat-failure | desktop-dark | `before-chat-failure-desktop-dark.png` | `after-A-inter-chat-failure-desktop-dark.png` | `after-B-geist-chat-failure-desktop-dark.png` |
| chat-failure | desktop-light | `before-chat-failure-desktop-light.png` | `after-A-inter-chat-failure-desktop-light.png` | `after-B-geist-chat-failure-desktop-light.png` |
| chat-failure | mobile-dark | `before-chat-failure-mobile-dark.png` | `after-A-inter-chat-failure-mobile-dark.png` | `after-B-geist-chat-failure-mobile-dark.png` |
| chat-list | mobile-dark | `before-chat-list-mobile-dark.png` | `after-A-inter-chat-list-mobile-dark.png` | `after-B-geist-chat-list-mobile-dark.png` |
| chat-middle | desktop-dark | `before-chat-middle-desktop-dark.png` | `after-A-inter-chat-middle-desktop-dark.png` | `after-B-geist-chat-middle-desktop-dark.png` |
| chat-middle | desktop-light | `before-chat-middle-desktop-light.png` | `after-A-inter-chat-middle-desktop-light.png` | `after-B-geist-chat-middle-desktop-light.png` |
| chat-short | desktop-dark | `before-chat-short-desktop-dark.png` | `after-A-inter-chat-short-desktop-dark.png` | `after-B-geist-chat-short-desktop-dark.png` |
| chat-short | desktop-light | `before-chat-short-desktop-light.png` | `after-A-inter-chat-short-desktop-light.png` | `after-B-geist-chat-short-desktop-light.png` |
| chat-tools | desktop-dark | `before-chat-tools-desktop-dark.png` | `after-A-inter-chat-tools-desktop-dark.png` | `after-B-geist-chat-tools-desktop-dark.png` |
| chat-tools | desktop-light | `before-chat-tools-desktop-light.png` | `after-A-inter-chat-tools-desktop-light.png` | `after-B-geist-chat-tools-desktop-light.png` |
| chat-tools | mobile-dark | `before-chat-tools-mobile-dark.png` | `after-A-inter-chat-tools-mobile-dark.png` | `after-B-geist-chat-tools-mobile-dark.png` |
| chat-tools-edits | desktop-dark | `before-chat-tools-edits-desktop-dark.png` | `after-A-inter-chat-tools-edits-desktop-dark.png` | `after-B-geist-chat-tools-edits-desktop-dark.png` |
| chat-tools-edits | desktop-light | `before-chat-tools-edits-desktop-light.png` | `after-A-inter-chat-tools-edits-desktop-light.png` | `after-B-geist-chat-tools-edits-desktop-light.png` |
| chat-tools-open | desktop-dark | `before-chat-tools-open-desktop-dark.png` | `after-A-inter-chat-tools-open-desktop-dark.png` | `after-B-geist-chat-tools-open-desktop-dark.png` |
| chat-tools-open | desktop-light | `before-chat-tools-open-desktop-light.png` | `after-A-inter-chat-tools-open-desktop-light.png` | `after-B-geist-chat-tools-open-desktop-light.png` |
| chat-top | desktop-dark | `before-chat-top-desktop-dark.png` | `after-A-inter-chat-top-desktop-dark.png` | `after-B-geist-chat-top-desktop-dark.png` |
| chat-top | desktop-light | `before-chat-top-desktop-light.png` | `after-A-inter-chat-top-desktop-light.png` | `after-B-geist-chat-top-desktop-light.png` |
| environment | desktop-dark | `before-environment-desktop-dark.png` | `after-A-inter-environment-desktop-dark.png` | `after-B-geist-environment-desktop-dark.png` |
| environment | desktop-light | `before-environment-desktop-light.png` | `after-A-inter-environment-desktop-light.png` | `after-B-geist-environment-desktop-light.png` |
| environment | mobile-dark | `before-environment-mobile-dark.png` | `after-A-inter-environment-mobile-dark.png` | `after-B-geist-environment-mobile-dark.png` |
| home-sidebar | desktop-dark | `before-home-sidebar-desktop-dark.png` | `after-A-inter-home-sidebar-desktop-dark.png` | `after-B-geist-home-sidebar-desktop-dark.png` |
| home-sidebar | desktop-light | `before-home-sidebar-desktop-light.png` | `after-A-inter-home-sidebar-desktop-light.png` | `after-B-geist-home-sidebar-desktop-light.png` |
| observability-errors | desktop-dark | `before-observability-errors-desktop-dark.png` | `after-A-inter-observability-errors-desktop-dark.png` | `after-B-geist-observability-errors-desktop-dark.png` |
| observability-errors | desktop-light | `before-observability-errors-desktop-light.png` | `after-A-inter-observability-errors-desktop-light.png` | `after-B-geist-observability-errors-desktop-light.png` |
| observability-errors | mobile-dark | `before-observability-errors-mobile-dark.png` | `after-A-inter-observability-errors-mobile-dark.png` | `after-B-geist-observability-errors-mobile-dark.png` |
| observability-overview | desktop-dark | `before-observability-overview-desktop-dark.png` | `after-A-inter-observability-overview-desktop-dark.png` | `after-B-geist-observability-overview-desktop-dark.png` |
| observability-overview | desktop-light | `before-observability-overview-desktop-light.png` | `after-A-inter-observability-overview-desktop-light.png` | `after-B-geist-observability-overview-desktop-light.png` |
| observability-overview | mobile-dark | `before-observability-overview-mobile-dark.png` | `after-A-inter-observability-overview-mobile-dark.png` | `after-B-geist-observability-overview-mobile-dark.png` |
| runs | desktop-dark | `before-runs-desktop-dark.png` | `after-A-inter-runs-desktop-dark.png` | `after-B-geist-runs-desktop-dark.png` |
| runs | desktop-light | `before-runs-desktop-light.png` | `after-A-inter-runs-desktop-light.png` | `after-B-geist-runs-desktop-light.png` |
| runs | mobile-dark | `before-runs-mobile-dark.png` | `after-A-inter-runs-mobile-dark.png` | `after-B-geist-runs-mobile-dark.png` |
| settings-appearance | desktop-dark | `before-settings-appearance-desktop-dark.png` | `after-A-inter-settings-appearance-desktop-dark.png` | `after-B-geist-settings-appearance-desktop-dark.png` |
| settings-appearance | desktop-light | `before-settings-appearance-desktop-light.png` | `after-A-inter-settings-appearance-desktop-light.png` | `after-B-geist-settings-appearance-desktop-light.png` |
| usage | desktop-dark | `before-usage-desktop-dark.png` | `after-A-inter-usage-desktop-dark.png` | `after-B-geist-usage-desktop-dark.png` |
| usage | desktop-light | `before-usage-desktop-light.png` | `after-A-inter-usage-desktop-light.png` | `after-B-geist-usage-desktop-light.png` |
| workflows | desktop-dark | `before-workflows-desktop-dark.png` | `after-A-inter-workflows-desktop-dark.png` | `after-B-geist-workflows-desktop-dark.png` |
| workflows | desktop-light | `before-workflows-desktop-light.png` | `after-A-inter-workflows-desktop-light.png` | `after-B-geist-workflows-desktop-light.png` |
| workflows | mobile-dark | `before-workflows-mobile-dark.png` | `after-A-inter-workflows-mobile-dark.png` | `after-B-geist-workflows-mobile-dark.png` |
