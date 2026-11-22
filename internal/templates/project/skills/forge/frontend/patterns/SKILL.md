---
name: patterns
description: Frontend component patterns — composition, container/presentational split, effects discipline, typed boundaries, and keeping components focused.
---

# Frontend Component Patterns

## Composition over prop drilling

When intermediate components don't need a value, pass rendered children as slots instead of threading props through layers:

```tsx
<PageLayout
  header={<PageHeader title={project.name} />}
  sidebar={<ProjectSidebar project={project} />}
  main={<ProjectView project={project} permissions={permissions} />}
/>
```

The layout places things — it doesn't understand projects, users, or permissions. This pattern applies to `Layout`, `Panel`, `Dialog`, `Toolbar`, `Card`, `EmptyState`, and similar structural components.

## Container vs presentational

Split data-fetching from rendering when it helps testability:

```tsx
// Container — fetches and coordinates
function WorkflowRunsContainer({ workflowId }: { workflowId: string }) {
  const runs = useListWorkflowRuns(workflowId);
  return <WorkflowRunsList runs={runs.data ?? []} isLoading={runs.isLoading} />;
}

// Presentational — renders props, no data fetching
function WorkflowRunsList({ runs, isLoading }: Props) { /* rendering only */ }
```

Don't be religious about it. Split when it makes the code clearer or the UI component reusable.

## Keep components focused

A component should do **one** of these well:

- Fetch/own data (container)
- Transform/coordinate (page)
- Render UI (presentational)
- Handle user interaction (form, button handler)

When one component does all four, extract pieces. The generated page templates follow this: the page coordinates, children specialize.

## Effects discipline

Use `useEffect` only for synchronization with external systems: event bus subscriptions, browser events, websockets, document title, imperative DOM APIs.

Never use effects to derive state:

```tsx
// BAD — effect to set derived state
useEffect(() => { setFullName(`${first} ${last}`); }, [first, last]);

// GOOD — derive during render
const fullName = `${first} ${last}`;
```

## Typed boundaries

Use TypeScript and schema validation at boundaries:

- **API responses** — generated Connect types handle this
- **Form values** — Zod schemas in create/edit pages
- **Event bus payloads** — typed event map in `src/lib/events.ts`
- **Route params** — validate with `z.string().uuid()` or similar

Do not assume external data has the expected shape.

## Reusable components: prefer controlled

For shared UI components, prefer controlled APIs so the parent can manage state:

```tsx
<Tabs value={activeTab} onValueChange={setActiveTab} />
```

Internal state is fine for leaf components, but core app components should be controllable.

## Styling Rules

- Use semantic component props/variants for repeated visual states instead of copy-pasting long class strings.
- Do not use `!important` to override generated or library styles; fix ownership/specificity instead.
- Avoid inline styles for normal UI. If dynamic styling is required, set a CSS variable inline and consume it from a class.
- Keep CSS modules/global CSS for structural patterns that Tailwind utilities cannot express cleanly.

## Rules

- Pass slots (JSX props) to avoid threading data through components that don't use it.
- Derive values during render — never `useEffect` to sync derived state.
- Every data-fetching component must handle loading, error, and empty states.
- Keep forms in `react-hook-form` + Zod — do not hand-roll form state with scattered `useState`.
