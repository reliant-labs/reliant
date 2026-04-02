# File Browser Component

A file browser for exploring and managing project files with syntax-highlighted previews.

## Features

### Tree View Navigation
- Hierarchical folder structure with expandable/collapsible folders
- File and folder icons with language-specific styling
- Click to navigate folders, click files to view content
- Diagnostic indicators (errors/warnings) for files and folders

### File Management
- **Inline Creation**: Create files and folders directly in the tree (no modals)
  - At root level via toolbar buttons
  - Within folders via context menu
  - Auto-focus input field with Enter to confirm, Escape to cancel
- **Toolbar Actions**: New File, New Folder, Refresh, Collapse All
- **Context Menu**: New File/Folder, Open, Copy Path/Relative Path/Name, Copy File, Delete
- **Hidden Files Toggle**: Show/hide dotfiles

### File Viewer
- Monaco editing for text/code files
- Inline image, PDF, audio, and video previews
- Binary fallback panel with metadata and reveal/copy/open actions
- File metadata display (size, preview kind, MIME type, modified date)

### Search
- Real-time search with matched filename highlighting
- Deep search across all files and folders
- Auto-expand folders containing matches

### Breadcrumb Navigation
- Shows current path with clickable segments
- Home button to return to root

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
└── README.md
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

Uses the app's theme system with CSS variables: `--background`, `--foreground`, `--muted`, `--border`, `--primary`.

## Supported File Types

The viewer routes files by preview kind using backend-provided `FilePreviewInfo` metadata.

- **Text/code**: Opens in Monaco editor (JS, TS, Python, Go, Rust, Java, C/C++, HTML/CSS, JSON/YAML, Markdown, etc.)
- **Inline previews**: Images (jpg, png, gif, webp, svg, etc.), PDFs, audio (mp3, wav, ogg), video (mp4, webm)
- **Binary fallback**: Unsupported binary formats render a metadata panel

### Text-only workflow safeguards
- Add-to-chat line selection only appears for text files
- Delete undo only snapshots text file content
- Monaco background sync skips non-text files
