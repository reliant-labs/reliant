# DiffViewer Component

Displays file diffs in unified diff format with collapsible file sections and line numbers.

## Usage

```tsx
import { DiffViewer } from './DiffViewer';

<DiffViewer
  diff={diffString}
  fileName="example.js"
  collapsible={true}
  defaultCollapsed={false}
/>
```

```tsx
<DiffViewer
  diff={diffString}
  fileName="example.js"
  collapsible={true}
  defaultCollapsed={false}
  maxHeight="70vh"
  className="my-custom-diff-viewer"
/>
```

## Props

| Prop | Type | Default | Description |
|------|------|---------|-------------|
| `diff` | `string` | **required** | The diff string in unified diff format |
| `fileName` | `string` | `undefined` | Optional filename for display |
| `className` | `string` | `undefined` | Additional CSS classes |
| `collapsible` | `boolean` | `true` | Whether file sections can be collapsed |
| `defaultCollapsed` | `boolean` | `false` | Default collapsed state for files |
| `maxHeight` | `string` | `'60vh'` | Maximum height of diff content |

## Automatic Diff Generation

The DiffViewer handles cases where no diff is provided:

- **New File Creation**: Generates `--- /dev/null +++ b/filename` format
- **File Editing**: Creates unified diff from old/new content
- **Bash Commands**: Parses `cat >` commands and generates diffs

```tsx
// New file creation
<DiffViewer
  diff={createNewFileDiff("new-component.tsx", fileContent)}
  fileName="new-component.tsx"
  collapsible={false}
  maxHeight="50vh"
/>
```
