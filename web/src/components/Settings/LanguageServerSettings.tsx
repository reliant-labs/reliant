/**
 * Language Server Settings Component
 * 
 * Allows users to:
 * - Enable/disable built-in language servers
 * - Add custom language servers for any language
 * - View installation instructions
 */

import { useState, useEffect } from 'react';
import { Plus, Trash2, ChevronDown, ChevronUp } from 'lucide-react';
import { cn } from '../../lib/utils';
import { useEditorStore } from '../../store/editorStore';

export interface LanguageServerConfig {
  id: string;
  name: string;
  language: string;
  command: string;
  args: string[];
  extensions: string[];
  enabled: boolean;
  isCustom?: boolean;
}

const DEFAULT_SERVERS: LanguageServerConfig[] = [
  {
    id: 'gopls',
    name: 'Go (gopls)',
    language: 'go',
    command: 'gopls',
    args: ['serve'],
    extensions: ['.go'],
    enabled: true,
  },
  {
    id: 'pyright',
    name: 'Python (pyright)',
    language: 'python',
    command: 'pyright-langserver',
    args: ['--stdio'],
    extensions: ['.py', '.pyi'],
    enabled: true,
  },
  {
    id: 'rust-analyzer',
    name: 'Rust (rust-analyzer)',
    language: 'rust',
    command: 'rust-analyzer',
    args: [],
    extensions: ['.rs'],
    enabled: false,
  },
  {
    id: 'clangd',
    name: 'C/C++ (clangd)',
    language: 'c',
    command: 'clangd',
    args: [],
    extensions: ['.c', '.cpp', '.h', '.hpp', '.cc', '.cxx'],
    enabled: false,
  },
  {
    id: 'typescript-language-server',
    name: 'TypeScript (tsserver)',
    language: 'typescript',
    command: 'typescript-language-server',
    args: ['--stdio'],
    extensions: ['.ts', '.tsx', '.js', '.jsx'],
    enabled: false, // Monaco has built-in TS support
  },
];

interface Props {
  compact?: boolean; // For embedding in other settings pages
}

export function LanguageServerSettings({ compact = false }: Props) {
  const languageServers = useEditorStore((state) => state.languageServers) || DEFAULT_SERVERS;
  const updateLanguageServers = useEditorStore((state) => state.updateLanguageServers);
  
  const [servers, setServers] = useState<LanguageServerConfig[]>(() => languageServers);
  const [showAddForm, setShowAddForm] = useState(false);
  const [showInstallInfo, setShowInstallInfo] = useState(false);
  const [hasUserModified, setHasUserModified] = useState(false);
  const [newServer, setNewServer] = useState<Partial<LanguageServerConfig>>({
    name: '',
    language: '',
    command: '',
    args: [],
    extensions: [],
    enabled: true,
    isCustom: true,
  });

  // Sync with store only when user makes changes
  useEffect(() => {
    if (hasUserModified && updateLanguageServers) {
      updateLanguageServers(servers);
    }
  }, [servers, hasUserModified, updateLanguageServers]);

  const toggleServer = (id: string) => {
    setHasUserModified(true);
    setServers(prev =>
      prev.map(s =>
        s.id === id ? { ...s, enabled: !s.enabled } : s
      )
    );
  };

  const removeServer = (id: string) => {
    setHasUserModified(true);
    setServers(prev => prev.filter(s => s.id !== id));
  };

  const addServer = () => {
    if (!newServer.name || !newServer.command || !newServer.language) return;

    const id = `custom-${Date.now()}`;
    const server: LanguageServerConfig = {
      id,
      name: newServer.name,
      language: newServer.language,
      command: newServer.command,
      args: newServer.args || [],
      extensions: newServer.extensions || [],
      enabled: true,
      isCustom: true,
    };

    setHasUserModified(true);
    setServers(prev => [...prev, server]);
    setNewServer({
      name: '',
      language: '',
      command: '',
      args: [],
      extensions: [],
      enabled: true,
      isCustom: true,
    });
    setShowAddForm(false);
  };

  const INSTALL_COMMANDS: Record<string, string> = {
    gopls: 'go install golang.org/x/tools/gopls@latest',
    pyright: 'npm install -g pyright',
    'rust-analyzer': 'rustup component add rust-analyzer',
    clangd: 'brew install llvm  # macOS\nsudo apt install clangd  # Ubuntu/Debian',
    'typescript-language-server': 'npm install -g typescript typescript-language-server',
  };

  return (
    <div className={cn("space-y-4", !compact && "space-y-6")}>
      {!compact && (
        <div>
          <h3 className="text-lg font-semibold mb-1">Language Servers</h3>
          <p className="text-sm text-muted-foreground">
            Enable language servers for enhanced editor features like Go to Definition,
            hover information, and autocomplete.
          </p>
        </div>
      )}

      {/* Server List */}
      <div className="space-y-2">
        {servers.map(server => (
          <div
            key={server.id}
            className={cn(
              "flex items-center justify-between p-3 rounded-lg border transition-colors",
              server.enabled 
                ? "border-primary/30 bg-primary/5" 
                : "border-border bg-muted/10"
            )}
          >
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <span className={cn(
                  "font-medium text-sm truncate",
                  !server.enabled && "text-muted-foreground"
                )}>
                  {server.name}
                </span>
                {server.isCustom && (
                  <span className="text-xs px-1.5 py-0.5 rounded bg-muted text-muted-foreground">
                    Custom
                  </span>
                )}
              </div>
              <p className="text-xs text-muted-foreground truncate mt-0.5">
                {server.extensions.length > 0
                  ? server.extensions.join(', ')
                  : server.language}
              </p>
            </div>

            <div className="flex items-center gap-2 ml-2">
              {server.isCustom && (
                <button
                  onClick={() => removeServer(server.id)}
                  className="p-1.5 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors"
                  title="Remove"
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              )}
              <button
                onClick={() => toggleServer(server.id)}
                className={cn(
                  "px-3 py-1.5 text-xs font-medium rounded-md transition-colors",
                  server.enabled
                    ? "bg-primary text-primary-foreground hover:bg-primary/90"
                    : "bg-muted text-muted-foreground hover:bg-muted/80"
                )}
              >
                {server.enabled ? 'On' : 'Off'}
              </button>
            </div>
          </div>
        ))}
      </div>

      {/* Add Custom Server */}
      <div className="space-y-3">
        {!showAddForm ? (
          <button
            onClick={() => setShowAddForm(true)}
            className="flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground transition-colors"
          >
            <Plus className="w-4 h-4" />
            Add custom language server
          </button>
        ) : (
          <div className="p-4 rounded-lg border border-border bg-card space-y-3">
            <h4 className="text-sm font-medium">Add Custom Language Server</h4>
            
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="text-xs text-muted-foreground block mb-1">Name</label>
                <input
                  type="text"
                  value={newServer.name}
                  onChange={(e) => setNewServer(prev => ({ ...prev, name: e.target.value }))}
                  placeholder="e.g., Lua Language Server"
                  className="w-full px-3 py-2 text-sm bg-background border border-border rounded-md focus:outline-none focus:ring-2 focus:ring-primary/50"
                />
              </div>
              <div>
                <label className="text-xs text-muted-foreground block mb-1">Language ID</label>
                <input
                  type="text"
                  value={newServer.language}
                  onChange={(e) => setNewServer(prev => ({ ...prev, language: e.target.value }))}
                  placeholder="e.g., lua"
                  className="w-full px-3 py-2 text-sm bg-background border border-border rounded-md focus:outline-none focus:ring-2 focus:ring-primary/50"
                />
              </div>
            </div>

            <div>
              <label className="text-xs text-muted-foreground block mb-1">Command</label>
              <input
                type="text"
                value={newServer.command}
                onChange={(e) => setNewServer(prev => ({ ...prev, command: e.target.value }))}
                placeholder="e.g., lua-language-server"
                className="w-full px-3 py-2 text-sm bg-background border border-border rounded-md focus:outline-none focus:ring-2 focus:ring-primary/50"
              />
            </div>

            <div>
              <label className="text-xs text-muted-foreground block mb-1">File Extensions (comma-separated)</label>
              <input
                type="text"
                value={newServer.extensions?.join(', ')}
                onChange={(e) => setNewServer(prev => ({ 
                  ...prev, 
                  extensions: e.target.value.split(',').map(s => s.trim()).filter(Boolean)
                }))}
                placeholder="e.g., .lua"
                className="w-full px-3 py-2 text-sm bg-background border border-border rounded-md focus:outline-none focus:ring-2 focus:ring-primary/50"
              />
            </div>

            <div className="flex items-center justify-end gap-2 pt-2">
              <button
                onClick={() => setShowAddForm(false)}
                className="px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={addServer}
                disabled={!newServer.name || !newServer.command || !newServer.language}
                className="px-4 py-1.5 text-sm font-medium bg-primary text-primary-foreground rounded-md hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                Add Server
              </button>
            </div>
          </div>
        )}
      </div>

      {/* Installation Info */}
      <div className="pt-2">
        <button
          onClick={() => setShowInstallInfo(!showInstallInfo)}
          className="flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground transition-colors"
        >
          {showInstallInfo ? <ChevronUp className="w-4 h-4" /> : <ChevronDown className="w-4 h-4" />}
          Installation instructions
        </button>
        
        {showInstallInfo && (
          <div className="mt-3 p-4 rounded-lg border border-border bg-muted/20 space-y-3">
            <p className="text-xs text-muted-foreground">
              Language servers must be installed on your system. Here are the installation commands:
            </p>
            <div className="space-y-2">
              {Object.entries(INSTALL_COMMANDS).map(([id, cmd]) => {
                const server = servers.find(s => s.id === id);
                if (!server) return null;
                return (
                  <div key={id} className="flex items-start gap-2 text-xs">
                    <span className="font-medium text-foreground shrink-0 w-24">{server.name.split(' ')[0]}:</span>
                    <code className="text-muted-foreground bg-muted px-2 py-1 rounded whitespace-pre-wrap">{cmd}</code>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// Compact version for embedding in other settings
export function LanguageServerSettingsCompact() {
  return <LanguageServerSettings compact />;
}
