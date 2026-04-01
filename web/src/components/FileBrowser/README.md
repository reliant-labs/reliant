# File Browser Component

A modern, full-featured file browser component for exploring and managing project files with syntax-highlighted previews.

## Features

### 🌳 Tree View Navigation
- Hierarchical folder structure
- Expandable/collapsible folders
- File and folder icons with language-specific styling
- Visual indentation for nested items
- Click to navigate folders, click files to view content
- Auto-open files in viewer tabs
- Diagnostic indicators (errors/warnings) for files and folders

### 🛠️ File Management
- **Inline Creation**: Create files and folders directly in the tree (no modals)
  - At root level via toolbar buttons
  - Within folders via context menu
  - Auto-focus input field with Enter to confirm, Escape to cancel
- **Toolbar Actions**: Quick access buttons for common operations
  - New File (FilePlus icon)
  - New Folder (FolderPlus icon)
  - Refresh tree (RefreshCw icon)
  - Collapse all folders (ChevronsDownUp icon)
- **Context Menu**: Right-click operations
  - New File / New Folder (for directories)
  - Open (for files)
  - Copy Path / Copy Relative Path / Copy Name
  - Copy File (duplicate file with new name)
  - Delete (with confirmation)
- **Hidden Files Toggle**: Show/hide files starting with `.`
- **Auto-Open**: Newly created files automatically open in viewer tabs

### 👀 File Viewer
- Monaco editing for text/code files
- Inline image previews
- Inline PDF previews
- Native audio/video playback for supported media
- Binary fallback panel with metadata and reveal/copy/open actions
- File metadata display (size, preview kind, MIME type, modified date)

### 🔍 Search & Filter
- Real-time search as you type
- Highlights matching file names in tree
- Deep search across all files and folders (includes children)
- Auto-expand folders containing matches
- Clear button to reset search

### 🍞 Breadcrumb Navigation
- Shows current path
- Click any segment to jump to that folder
- Home button to return to root

## Usage

```tsx
import { FileBrowser } from './components/FileBrowser';

function App() {
  return <FileBrowser />;
}
```

## Component Structure

```
FileBrowser/
├── index.tsx                  # Main container component
├── RightSidebar.tsx           # Right sidebar with tabs (Files, Changes, Tasks, Processes, Browser)
├── FileTree.tsx               # Tree view with inline creation at root
├── FileTreeItem.tsx           # Individual tree node with context menu & inline creation
├── FileTreeToolbar.tsx        # Toolbar with action buttons and project name
├── FileViewer.tsx             # File content viewer with syntax highlighting
├── FileOperationsModal.tsx    # Modal for copy/delete confirmations
├── Breadcrumbs.tsx            # Path navigation component
├── SearchBar.tsx              # Search input with clear button
└── README.md                 # This file
```

## API Integration

The File Browser uses the `fileSystem` API module:

- `getFileTree(path?)` - Fetches directory structure
- `getFileContent(path)` - Fetches text file contents
- `getFileMetadata(path)` - Fetches file/directory metadata
- `getFilePreviewInfo(path)` - Fetches canonical preview classification (`text`, `image`, `pdf`, `audio`, `video`, `binary`)
- `getFilePreviewBlob(path)` - Fetches raw preview bytes for blob-backed media rendering

Mock data is provided as fallback for development.

## Keyboard Shortcuts

- `Cmd/Ctrl + F` - Focus search (when in Files view)
- `Escape` - Clear search
- `Arrow Keys` - Navigate file tree

## Theming

The component uses the app's existing theme system with CSS variables:
- `--background` - Main background
- `--foreground` - Text color
- `--muted` - Secondary backgrounds
- `--border` - Border colors
- `--primary` - Accent color for active items

## Supported File Types

The viewer routes files by preview kind using backend-provided `FilePreviewInfo` metadata.

### Text / code → Monaco editor
Common source and text formats continue to open in Monaco, including:
- JavaScript/TypeScript (.js, .jsx, .ts, .tsx)
- Python (.py)
- Go (.go)
- Rust (.rs)
- Java (.java)
- C/C++ (.c, .cpp, .h)
- HTML/CSS (.html, .css, .scss)
- JSON/YAML (.json, .yml, .yaml)
- Markdown (.md)
- And many more text-based formats

### Inline previews
- **Images:** jpg, jpe, jpeg, png, bmp, gif, ico, webp, avif, svg
- **PDFs:** pdf
- **Audio:** mp3, wav, ogg, oga
- **Video:** mp4, webm

### Binary fallback
Unsupported binary formats (for example `.zip`) render a metadata panel instead of forcing raw bytes through the text editor.

### Text-only workflow safeguards
Text-oriented workflows now check preview kind first:
- add-to-chat line selection only appears for text files
- delete undo only snapshots text file content
- Monaco background sync skips non-text files

## Performance

- Lazy rendering of tree nodes
- Virtualized scrolling for large file lists
- Debounced search for smooth typing
- Code highlighting is cached per file

## Future Enhancements

- [ ] File upload/download
- [ ] Drag & drop file operations
- [ ] File rename/delete
- [ ] Multi-file selection
- [ ] Context menu for file operations
- [ ] Recent files history
- [ ] Favorites/bookmarks
- [ ] Split view for comparing files
