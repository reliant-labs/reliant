import { useEffect, useRef, useState } from 'react';
import { cn } from '../../lib/utils';
import type { ToolApprovalRequest } from '../../api/client';
import { DiffEditor } from '@monaco-editor/react';
import { getMonacoLanguage, configureMonacoTheme, getCurrentMonacoTheme } from '../../lib/monacoTheme';
import { useEditorStore } from '../../store/editorStore';
import { Shield, Check, X, Terminal, FileText, Code } from 'lucide-react';
import { monacoManager } from '../../lib/monacoManager';

interface PermissionPromptProps {
  request: ToolApprovalRequest;
  onApprove: (requestId: string) => void;
  onDeny: (requestId: string) => void;
}

const getToolIcon = (toolName: string) => {
  switch (toolName.toLowerCase()) {
    case 'bash':
    case 'powershell':
      return Terminal;
    case 'write':
    case 'edit':
    case 'patch':
      return FileText;
    default:
      return Code;
  }
};

const getToolColor = (toolName: string) => {
  switch (toolName.toLowerCase()) {
    case 'bash':
    case 'powershell':
      return 'text-destructive';
    case 'write':
    case 'edit':
    case 'patch':
      return '';
    default:
      return '';
  }
};

const getToolBgStyle = (toolName: string) => {
  switch (toolName.toLowerCase()) {
    case 'bash':
    case 'powershell':
      return { backgroundColor: 'hsl(var(--destructive) / 0.1)' };
    case 'write':
    case 'edit':
    case 'patch':
      return { backgroundColor: 'hsl(var(--primary) / 0.1)' };
    default:
      return { backgroundColor: 'hsl(var(--primary) / 0.1)' };
  }
};

// Monaco-based diff viewer component
function MonacoDiffViewer({
  original,
  modified,
  filename
}: {
  original: string;
  modified: string;
  filename: string;
}) {
  const settings = useEditorStore((state) => state.settings);
  const monacoRef = useRef<any>(null);
  const editorRef = useRef<any>(null);
  const [isCollapsed, setIsCollapsed] = useState(false);
  const [monacoInstance, setMonacoInstance] = useState<any>(null);

  // Get Monaco from cache
  useEffect(() => {
    let cancelled = false;
    monacoManager.getMonaco()
      .then(monaco => { if (!cancelled) setMonacoInstance(monaco); })
      .catch(err => console.error('Failed to get Monaco:', err));
    return () => { cancelled = true; };
  }, []);

  // Listen for theme changes and update Monaco editor
  useEffect(() => {
    const handleThemeChange = () => {
      if (monacoRef.current) {
        // Reconfigure theme to pick up light/dark mode changes
        configureMonacoTheme(monacoRef.current);
        const themeName = getCurrentMonacoTheme();
        monacoRef.current.editor.setTheme(themeName);
      }
    };

    window.addEventListener('theme-applied', handleThemeChange);
    window.addEventListener('appearance-updated', handleThemeChange);

    return () => {
      window.removeEventListener('theme-applied', handleThemeChange);
      window.removeEventListener('appearance-updated', handleThemeChange);
    };
  }, []);

  // Update editor options when settings change
  useEffect(() => {
    if (editorRef.current) {
      const modifiedEditor = editorRef.current.getModifiedEditor();
      const originalEditor = editorRef.current.getOriginalEditor();

      const options = {
        minimap: { enabled: settings.minimap },
        fontSize: settings.fontSize,
        lineNumbers: settings.lineNumbers ? 'on' : 'off',
        wordWrap: settings.wordWrap ? 'on' : 'off',
        renderWhitespace: settings.renderWhitespace ? 'all' : 'none',
        bracketPairColorization: { enabled: settings.bracketPairColorization },
        guides: {
          bracketPairs: settings.guides,
          indentation: settings.guides,
        },
        renderLineHighlight: settings.renderLineHighlight,
        tabSize: settings.tabSize,
      };

      modifiedEditor?.updateOptions(options);
      originalEditor?.updateOptions(options);
    }
  }, [settings]);

  const lineCount = modified.split('\n').length;
  const height = Math.min(Math.max(lineCount * 20, 200), 600);

  return (
    <div className="border border-border rounded-md overflow-hidden">
      {/* Header */}
      <div
        className="flex items-center justify-between px-3 py-2 border-b border-border bg-muted/30 cursor-pointer hover:bg-muted/50 transition-colors"
        onClick={() => setIsCollapsed(!isCollapsed)}
      >
        <div className="flex items-center gap-2">
          <FileText className="w-4 h-4 text-muted-foreground" />
          <span className="text-xs font-mono text-foreground/80 truncate">
            {filename}
          </span>
        </div>
        <span className="text-xs text-muted-foreground">
          {isCollapsed ? '▼' : '▲'}
        </span>
      </div>

      {/* Diff Editor */}
      {!isCollapsed && (
        <div style={{ height: `${height}px` }}>
          {!monacoInstance ? (
            <div className="flex items-center justify-center h-full bg-muted/30 text-xs text-muted-foreground">
              Loading editor...
            </div>
          ) : (
            <DiffEditor
              height="100%"
              language={getMonacoLanguage(filename)}
              original={original}
              modified={modified}
              theme={getCurrentMonacoTheme()}
              beforeMount={(monaco) => {
                monacoRef.current = monaco;
                configureMonacoTheme(monaco);
              }}
              onMount={(editor) => {
                editorRef.current = editor;
              }}
              options={{
                readOnly: true,
                minimap: { enabled: settings.minimap },
                fontSize: settings.fontSize,
                lineNumbers: settings.lineNumbers ? 'on' : 'off',
                wordWrap: settings.wordWrap ? 'on' : 'off',
                automaticLayout: true,
                scrollBeyondLastLine: false,
                renderWhitespace: settings.renderWhitespace ? 'all' : 'none',
                bracketPairColorization: { enabled: settings.bracketPairColorization },
                guides: {
                  bracketPairs: settings.guides,
                  indentation: settings.guides,
                },
                renderLineHighlight: settings.renderLineHighlight,
                renderSideBySide: true,
                ignoreTrimWhitespace: false,
                renderIndicators: true,
                originalEditable: false,
                diffCodeLens: false,
              }}
              loading={null}
            />
          )}
        </div>
      )}
    </div>
  );
}

export function PermissionPrompt({ request, onApprove, onDeny }: PermissionPromptProps) {
  const Icon = getToolIcon(request.tool_name || 'unknown');
  const colorClass = getToolColor(request.tool_name || 'unknown');

  // Detect platform for keyboard shortcut hint
  const isMac = typeof window !== 'undefined' &&
    (window.navigator.platform.toUpperCase().includes('MAC') ||
     window.navigator.userAgent.toUpperCase().includes('MAC'));
  const shortcutKey = isMac ? '⌘' : 'Ctrl';

  // Extract parameters for different tool types
  const params = request.params as Record<string, unknown>;
  const filePath = (params?.file_path || params?.FilePath) as string | undefined;
  const content = (params?.content || params?.Content) as string | undefined;
  const oldString = (params?.old_string || params?.OldString) as string | undefined;
  const newString = (params?.new_string || params?.NewString) as string | undefined;
  const patchText = (params?.patch_text || params?.PatchText) as string | undefined;
  const edits = (params?.edits || params?.Edits) as any[] | undefined;

  // Determine what to show
  let originalContent = '';
  let modifiedContent = '';
  let displayFilePath = filePath || 'file';
  let showDiff = false;

  // Handle different tool types
  if (request.tool_name?.toLowerCase() === 'write' && content) {
    // New file creation - empty original, content as modified
    originalContent = '';
    modifiedContent = content;
    showDiff = true;
  } else if (request.tool_name?.toLowerCase() === 'edit') {
    if (edits && edits.length > 0) {
      // Multiple edits - show the first one for now
      const firstEdit = edits[0];
      displayFilePath = firstEdit.file_path || filePath || 'file';
      originalContent = firstEdit.old_string || '';
      modifiedContent = firstEdit.new_string || '';
      showDiff = true;
    } else if (oldString !== undefined && newString !== undefined) {
      // Single edit
      originalContent = oldString;
      modifiedContent = newString;
      showDiff = true;
    }
  } else if (request.tool_name?.toLowerCase() === 'patch' && patchText) {
    // For patch, try to extract file changes from the patch text
    const fileMatch = patchText.match(/\*\*\* (?:Update|Add) File: (.+?)\n/);
    if (fileMatch) {
      displayFilePath = fileMatch[1];
    }
    // For now, show the patch text as modified content
    originalContent = '';
    modifiedContent = patchText;
    showDiff = true;
  }

  return (
    <div className="border rounded-lg p-4 mb-2" style={{ borderColor: 'hsl(var(--primary) / 0.3)', backgroundColor: 'hsl(var(--primary) / 0.1)' }}>
      <div className="flex items-start gap-3">
        <div className={cn("p-2 rounded-lg", colorClass)} style={getToolBgStyle(request.tool_name || 'unknown')}>
          <Icon className="w-4 h-4" style={['bash', 'powershell'].includes(request.tool_name?.toLowerCase() ?? '') ? undefined : { color: 'hsl(var(--primary))' }} />
        </div>

        <div className="flex-1">
          <div className="flex items-center gap-2 mb-1">
            <Shield className="w-4 h-4" style={{ color: 'hsl(var(--primary))' }} />
            <span className="font-semibold text-sm" style={{ color: 'hsl(var(--primary))' }}>
              Permission Required
            </span>
          </div>

          <p className="text-sm font-mono mb-3">
            {request.description}
          </p>

          {/* Show Monaco diff viewer for file operations */}
          {showDiff ? (
            <div className="mb-3">
              <MonacoDiffViewer
                original={originalContent}
                modified={modifiedContent}
                filename={displayFilePath}
              />
              {edits && edits.length > 1 && (
                <div className="mt-2 px-3 py-2 bg-muted/30 rounded-md text-xs text-muted-foreground font-mono">
                  Note: {edits.length} files will be modified. Showing first file.
                </div>
              )}
            </div>
          ) : (
            /* Fallback to raw params for non-file operations */
            <div className="bg-muted/20 rounded-md p-3 mb-3 border border-border">
              <pre className="text-xs font-mono overflow-x-auto text-foreground/80">
                {typeof request.params === 'object'
                  ? JSON.stringify(request.params, null, 2)
                  : request.params}
              </pre>
            </div>
          )}

          <div className="flex gap-2">
            <button
              onClick={() => onApprove(request.id)}
              className="flex items-center gap-2 px-3 py-1.5 rounded-md text-sm font-medium transition-colors"
              style={{
                backgroundColor: 'hsl(var(--success))',
                color: 'hsl(var(--success-foreground))'
              }}
              onMouseEnter={(e) => e.currentTarget.style.opacity = '0.9'}
              onMouseLeave={(e) => e.currentTarget.style.opacity = '1'}
            >
              <Check className="w-3.5 h-3.5" />
              Approve
              <span className="ml-1 px-1.5 py-0.5 rounded text-xs font-mono" style={{
                backgroundColor: 'hsl(var(--success-foreground) / 0.2)',
                color: 'hsl(var(--success-foreground))'
              }}>
                {shortcutKey}+↵
              </span>
            </button>
            <button
              onClick={() => onDeny(request.id)}
              className="flex items-center gap-1 px-3 py-1.5 rounded-md text-sm font-medium transition-colors"
              style={{
                backgroundColor: 'hsl(var(--destructive))',
                color: 'hsl(var(--destructive-foreground))'
              }}
              onMouseEnter={(e) => e.currentTarget.style.opacity = '0.9'}
              onMouseLeave={(e) => e.currentTarget.style.opacity = '1'}
            >
              <X className="w-3.5 h-3.5" />
              Deny
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}