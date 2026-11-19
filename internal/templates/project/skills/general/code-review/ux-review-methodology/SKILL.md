---
name: ux-review-methodology
description: UX review methodology — browser-based application auditing, accessibility testing, responsive verification, and structured UX reporting
---

# UX Review Methodology

## Your Tools

You have access to Chrome DevTools MCP tools:
- `navigate_page` — Load URLs in the browser
- `take_snapshot` — Get the accessibility tree (preferred over screenshots for structure)
- `take_screenshot` — Capture visual state
- `click` — Interact with elements
- `fill` — Enter text into form fields
- `press_key` — Keyboard interactions
- `hover` — Test hover states
- `list_console_messages` — Check for JS errors
- `list_network_requests` — Check for failed requests
- `evaluate_script` — Run JS for deeper inspection
- `emulate` — Test responsive viewports, dark mode, etc.

## Review Process

### Step 1: Load the Application
Navigate to the application URL provided in your instructions. Wait for it to load fully.

### Step 2: Check for Errors
Before anything visual, check the console for errors:
- List console messages and filter for errors/warnings
- Check network requests for failed (4xx/5xx) responses
- These are automatic failures — report them immediately

### Step 3: Visual Audit
Take screenshots and snapshots of key pages/states:
- Landing/home page
- Key user flows (navigation, forms, interactive elements)
- Loading states and error states if reachable
- Check visual consistency, alignment, spacing
- Verify text is readable and hierarchy is clear

### Step 4: Interaction Testing
Test core user journeys:
- Click through primary navigation
- Fill and submit forms
- Test interactive elements (dropdowns, modals, tooltips)
- Verify transitions and state changes work correctly
- Check that actions produce expected results

### Step 5: Accessibility Audit
Use snapshots (accessibility tree) to verify:
- Semantic HTML structure (headings, landmarks, lists)
- ARIA attributes where needed
- Keyboard navigability (tab through interactive elements)
- Focus indicators visible
- Form labels associated with inputs
- Alt text on images

### Step 6: Responsive Testing
Use emulate to test at key breakpoints:
- Mobile (375x667, hasTouch: true, isMobile: true)
- Tablet (768x1024)
- Desktop (1440x900)
Check that layouts adapt properly and no content overflows or becomes inaccessible.

### Step 7: Dark Mode (if applicable)
Use emulate with colorScheme: "dark" to verify dark mode support if the app implements it.

## Reporting Guidelines

- **Be specific**: Include element UIDs, coordinates, or selectors when referencing issues
- **Prioritize**: Critical functional bugs > accessibility violations > visual polish > suggestions
- **Evidence**: Take screenshots of issues when possible
- **Context**: Explain why something is a problem, not just what's wrong
- **Actionable**: Each issue should have a clear fix path
- **Proportional**: Keep suggestions proportional to the change. If the change is a minor adjustment, focus your review on whether the adjustment achieves its goal rather than conducting a full UX audit.

## What Constitutes a Failure

**Automatic fail (grade: fail)**:
- JavaScript errors in console
- Broken network requests to critical APIs
- Core user flows that don't work (buttons do nothing, forms break)
- Page doesn't load or shows blank/error state
- Critical accessibility violations (no keyboard nav, missing form labels on required fields)

**Pass with suggestions (grade: pass)**:
- Minor visual inconsistencies
- Nice-to-have accessibility improvements
- Performance suggestions
- Design polish items
