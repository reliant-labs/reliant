# Minimal DiffViewer Component

The `DiffViewer` component provides a clean, minimal interface for viewing file differences (diffs) in the Reliant application. It follows the design patterns of modern AI coding tools like Cursor, Claude Code CLI, and ChatGPT - focused, functional, and elegant without unnecessary complexity.

## Design Philosophy

- **Minimal & Clean**: No overwhelming controls or complex features
- **Focused Functionality**: Shows diffs clearly without distractions
- **Modern Aesthetics**: Clean typography, subtle colors, smooth interactions
- **Responsive**: Adapts gracefully to different screen sizes
- **Accessible**: Maintains usability while being visually clean

## Features

### 🎯 **Core Functionality**
- **Unified Diff View**: Clean, traditional diff format
- **File Collapsing**: Collapsible file sections for better organization
- **Line Numbers**: Clear old/new line number indicators
- **Visual Indicators**: Subtle color coding for additions, deletions, and context

### 🎨 **Design Elements**
- **Subtle Colors**: Soft backgrounds that don't overwhelm
- **Clean Typography**: Monospace font for code readability
- **Smooth Transitions**: Hover effects and animations
- **Dark Mode Support**: Consistent with the application's theme

### 📱 **User Experience**
- **Collapsible Sections**: Click to expand/collapse file diffs
- **Scrollable Content**: Handles large diffs gracefully
- **Clear Visual Hierarchy**: Easy to scan and understand changes
- **Responsive Layout**: Works on all device sizes

## Usage

### Basic Usage

```tsx
import { DiffViewer } from './DiffViewer';

<DiffViewer
  diff={diffString}
  fileName="example.js"
  collapsible={true}
  defaultCollapsed={false}
/>
```

### With Custom Styling

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

## Visual Design

### Color Scheme
- **Additions (+)**: Subtle green background with green border
- **Deletions (-)**: Subtle red background with red border
- **Context**: Clean white/dark background
- **Hunk Headers**: Soft blue background for section markers

### Typography
- **Font**: Monospace for code readability
- **Size**: `text-sm` for optimal reading
- **Line Height**: Comfortable spacing for scanning

### Layout
- **Line Numbers**: Fixed-width columns for alignment
- **Content**: Flexible width with horizontal scroll when needed
- **Spacing**: Consistent padding and margins throughout

## Accessibility

- **Keyboard Navigation**: Full keyboard support for collapsible sections
- **Screen Reader**: Proper ARIA labels and descriptions
- **Focus Management**: Clear focus indicators for interactive elements
- **High Contrast**: Maintains readability in all theme modes

## Performance

- **Efficient Rendering**: Only renders visible content
- **Smooth Scrolling**: Optimized for large diffs
- **Memory Conscious**: Minimal state management
- **Fast Parsing**: Efficient diff parsing algorithm

## Browser Support

- **Modern Browsers**: Chrome, Firefox, Safari, Edge
- **ES6+ Features**: Uses modern JavaScript features
- **CSS Grid**: Responsive layout support
- **Touch Support**: Mobile-friendly interactions

## Examples

### File Creation Diff

```tsx
// For new file creation, the component automatically generates a synthetic diff
<DiffViewer
  diff={createNewFileDiff("new-component.tsx", fileContent)}
  fileName="new-component.tsx"
  collapsible={false}
  maxHeight="50vh"
/>
```

### Automatic Diff Generation

The DiffViewer now automatically handles cases where no diff is provided:

- **New File Creation**: Generates `--- /dev/null +++ b/filename` format
- **File Editing**: Creates unified diff from old/new content
- **Bash Commands**: Parses `cat >` commands and generates diffs

### File Modification Diff

```tsx
<DiffViewer
  diff={modifiedFileDiff}
  fileName="existing-file.js"
  collapsible={true}
  defaultCollapsed={true}
/>
```

## Migration from Complex Version

If you're upgrading from the previous complex version:

1. **Remove Complex Props**: No more `enableProgressiveLoading`, `enableSearch`, etc.
2. **Simplify Usage**: Focus on essential props only
3. **Cleaner Interface**: Enjoy the simplified, focused design
4. **Better Performance**: Lighter component with faster rendering

## Future Considerations

The minimal design allows for future enhancements while maintaining the clean aesthetic:

- **Syntax Highlighting**: Language-specific code coloring
- **Inline Comments**: Support for diff comments
- **Export Options**: Simple export functionality
- **Keyboard Shortcuts**: Enhanced navigation controls

## Contributing

When contributing to the DiffViewer component:

1. **Maintain Simplicity**: Keep the design clean and focused
2. **Test Readability**: Ensure diffs are easy to scan
3. **Preserve Performance**: Keep rendering fast and efficient
4. **Follow Design System**: Use consistent colors and spacing
5. **Accessibility First**: Maintain usability for all users

## Design Inspiration

This component draws inspiration from:

- **Cursor**: Clean, minimal diff interface
- **Claude Code CLI**: Focused, distraction-free design
- **ChatGPT**: Simple, elegant code presentation
- **GitHub**: Clear, readable diff formatting
- **VS Code**: Professional, accessible design patterns
