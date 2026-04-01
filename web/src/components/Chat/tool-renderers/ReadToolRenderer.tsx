/**
 * Renderer for read-only tools (view, grep, glob, ls, websearch, etc.)
 * Shows results with clickable file links
 */

import { memo } from 'react';
import type { ToolContentProps } from './types';
import { LightweightCodeViewer } from '../LightweightCodeViewer';
import { FileLink } from '../FileLink';
import { parseFilePath } from '../../../lib/filePath';
import { openFile } from '../../../lib/fileOpener';

// Interface for parsed websearch results
interface WebSearchResult {
  title: string;
  url: string;
  description: string;
}

// Show input parameters during execution
function ExecutingView({ toolName, input }: { toolName: string; input: Record<string, unknown> | string }) {
  const inputObj = typeof input === 'string' ? {} : input;

  let displayText = '';

  switch (toolName) {
    case 'read':
      const filePath = (inputObj.file_path || inputObj.FilePath) as string;
      const offset = inputObj.offset as number | undefined;
      const limit = inputObj.limit as number | undefined;
      displayText = `Reading: ${filePath || 'file'}`;
      if (offset !== undefined) {
        displayText += ` (starting at line ${offset})`;
      }
      if (limit !== undefined) {
        displayText += ` (${limit} lines)`;
      }
      break;

    case 'grep':
      const pattern = inputObj.pattern as string;
      const glob = inputObj.glob as string | undefined;
      const type = inputObj.type as string | undefined;
      displayText = `Searching for: "${pattern || 'pattern'}"`;
      if (glob) displayText += ` in ${glob}`;
      if (type) displayText += ` (${type} files)`;
      break;

    case 'glob':
      const globPattern = inputObj.pattern as string;
      displayText = `Finding files: ${globPattern || 'pattern'}`;
      break;

    case 'websearch':
      const query = inputObj.query as string;
      displayText = `Searching: "${query || 'query'}"`;
      break;

    default:
      displayText = 'Executing...';
  }

  return (
    <div className="tool-content-read">
      <div className="px-2 py-1.5 text-[11px] text-muted-foreground italic">
        {displayText}
      </div>
    </div>
  );
}

function ReadToolRendererComponent({ ctx }: ToolContentProps) {
  const { toolName, input, result, worktreeId, isExecuting } = ctx;
  const toolNameLower = toolName.toLowerCase();

  // During execution, show input parameters
  if (!result?.content) {
    if (isExecuting && input) {
      return <ExecutingView toolName={toolNameLower} input={input} />;
    }
    return null;
  }

  const content = result.content.trim();

  // Grep output formatter
  if (toolNameLower === 'grep') {
    return <GrepOutput content={content} input={input} worktreeId={worktreeId} />;
  }

  // Glob output formatter
  if (toolNameLower === 'glob') {
    return <GlobOutput content={content} worktreeId={worktreeId} />;
  }

  // Websearch output formatter
  if (toolNameLower === 'websearch') {
    return <WebsearchOutput content={content} />;
  }

  // LS / find_files output
  if (toolNameLower === 'ls' || toolNameLower === 'find_files') {
    return <FileListOutput content={content} worktreeId={worktreeId} />;
  }

  // Diagnostics
  if (toolNameLower === 'diagnostics') {
    return (
      <div className="tool-content-read">
        <LightweightCodeViewer
          content={content}
          language="text"
          maxHeight={300}
          minHeight={0}
          showLineNumbers={false}
          noBorder
        />
      </div>
    );
  }

  // Default: code viewer
  return (
    <div className="tool-content-read">
      <LightweightCodeViewer
        content={content}
        language="text"
        maxHeight={300}
        minHeight={0}
        noBorder
      />
    </div>
  );
}

// Grep output with file links and line numbers
function GrepOutput({ 
  content, 
  input: _input, 
  worktreeId 
}: { 
  content: string; 
  input: Record<string, unknown> | string;
  worktreeId?: string;
}) {
  const lines = content.split('\n');
  
  // Check if this is files_with_matches mode
  if (lines[0]?.startsWith('Found ') && lines[0]?.includes('files')) {
    const filePaths = lines.slice(1).filter(line => line.trim().length > 0);
    
    if (filePaths.length === 0) {
      return <div className="px-2 py-1.5 text-[11px] text-muted-foreground">No matches found</div>;
    }
    
    return (
      <div className="tool-content-grep">
        <div className="px-2 py-1 text-[10px] text-muted-foreground border-b border-border/30">
          {lines[0]}
        </div>
        <div className="px-2 py-1.5 space-y-0.5 max-h-[200px] overflow-y-auto">
          {filePaths.map((filePath, idx) => {
            const parsed = parseFilePath(filePath);
            if (!parsed) {
              return <div key={idx} className="text-[11px] font-mono">{filePath}</div>;
            }
            return (
              <div key={idx}>
                <FileLink
                  path={filePath}
                  showIcon={true}
                  worktreeId={worktreeId}
                  className="text-[11px]"
                >
                  {filePath}
                </FileLink>
              </div>
            );
          })}
        </div>
      </div>
    );
  }

  // Content mode with file:line:content format
  const hasFileLineFormat = lines.some(line => {
    const parts = line.split(':');
    return parts.length >= 3 && !isNaN(parseInt(parts[1], 10));
  });

  if (hasFileLineFormat) {
    const fileMatches = new Map<string, Array<{line: number, content: string}>>();
    
    lines.forEach(line => {
      const match = line.match(/^([^:]+):(\d+):(.*)$/);
      if (match) {
        const [, filePath, lineNum, content] = match;
        if (!fileMatches.has(filePath)) {
          fileMatches.set(filePath, []);
        }
        fileMatches.get(filePath)!.push({
          line: parseInt(lineNum, 10),
          content: content
        });
      }
    });

    if (fileMatches.size > 0) {
      return (
        <div className="tool-content-grep max-h-[200px] overflow-y-auto">
          {Array.from(fileMatches.entries()).map(([filePath, matches], idx) => (
            <div key={idx} className="border-b border-border/20 last:border-0">
              <div className="px-2 py-1 bg-muted/30 flex items-center gap-2">
                <FileLink
                  path={filePath}
                  showIcon={true}
                  worktreeId={worktreeId}
                  className="text-[11px] font-medium"
                />
                <span className="text-[10px] text-muted-foreground">
                  ({matches.length} match{matches.length !== 1 ? 'es' : ''})
                </span>
              </div>
              <div className="px-2 py-1 space-y-0.5">
                {matches.slice(0, 5).map((match, mIdx) => (
                  <div key={mIdx} className="text-[11px] font-mono flex items-start gap-1">
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        const pathWithLine = `${filePath}:${match.line}`;
                        const parsed = parseFilePath(pathWithLine);
                        if (parsed) {
                          openFile(parsed, worktreeId);
                        }
                      }}
                      className="text-info hover:underline cursor-pointer shrink-0"
                    >
                      L{match.line}
                    </button>
                    <span className="text-muted-foreground truncate">{match.content}</span>
                  </div>
                ))}
                {matches.length > 5 && (
                  <div className="text-[10px] text-muted-foreground italic">
                    ... {matches.length - 5} more
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>
      );
    }
  }

  // Fallback to code viewer
  return (
    <div className="tool-content-grep">
      <LightweightCodeViewer
        content={content}
        language="text"
        maxHeight={200}
        minHeight={0}
        showLineNumbers={false}
        noBorder
      />
    </div>
  );
}

// Glob output with file links
function GlobOutput({ content, worktreeId }: { content: string; worktreeId?: string }) {
  const lines = content.split('\n').filter(line => line.trim().length > 0);
  const filePaths = lines.filter(line => line.includes('/') || line.includes('.'));

  if (filePaths.length === 0) {
    return (
      <div className="tool-content-glob">
        <LightweightCodeViewer
          content={content}
          language="text"
          maxHeight={200}
          minHeight={0}
          showLineNumbers={false}
          noBorder
        />
      </div>
    );
  }

  return (
    <div className="tool-content-glob">
      <div className="px-2 py-1 text-[10px] text-muted-foreground border-b border-border/30">
        Found {filePaths.length} file{filePaths.length !== 1 ? 's' : ''}
      </div>
      <div className="px-2 py-1.5 space-y-0.5 max-h-[200px] overflow-y-auto">
        {filePaths.map((filePath, idx) => {
          const parsed = parseFilePath(filePath);
          if (!parsed) {
            return <div key={idx} className="text-[11px] font-mono">{filePath}</div>;
          }
          return (
            <div key={idx}>
              <FileLink
                path={filePath}
                showIcon={true}
                worktreeId={worktreeId}
                className="text-[11px]"
              />
            </div>
          );
        })}
      </div>
    </div>
  );
}

// File list output (ls, find_files)
function FileListOutput({ content, worktreeId }: { content: string; worktreeId?: string }) {
  const lines = content.split('\n').filter(line => line.trim().length > 0);

  return (
    <div className="tool-content-files">
      <div className="px-2 py-1.5 space-y-0.5 max-h-[200px] overflow-y-auto">
        {lines.map((line, idx) => {
          const parsed = parseFilePath(line);
          if (!parsed) {
            return <div key={idx} className="text-[11px] font-mono">{line}</div>;
          }
          return (
            <div key={idx}>
              <FileLink
                path={line}
                showIcon={true}
                worktreeId={worktreeId}
                className="text-[11px]"
              />
            </div>
          );
        })}
      </div>
    </div>
  );
}

// Websearch output with clickable URLs
function WebsearchOutput({ content }: { content: string }) {
  const lines = content.split('\n');
  const results: WebSearchResult[] = [];
  let currentResult: Partial<WebSearchResult> | null = null;
  let queryText = '';
  let resultCount = '';

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    
    if (line.startsWith('Query:')) {
      queryText = line.replace('Query:', '').replace(/\*\*/g, '').trim();
      continue;
    }
    
    if (line.startsWith('Results:')) {
      resultCount = line.replace('Results:', '').trim();
      continue;
    }
    
    const resultMatch = line.match(/^##\s*\d+\.\s*(.+)$/);
    if (resultMatch) {
      if (currentResult && currentResult.title) {
        results.push(currentResult as WebSearchResult);
      }
      currentResult = {
        title: resultMatch[1],
        url: '',
        description: ''
      };
      continue;
    }
    
    if (currentResult && line.startsWith('**URL:**')) {
      currentResult.url = line.replace('**URL:**', '').trim();
      continue;
    }
    
    if (line === '---' || line.startsWith('# ') || line.startsWith('Source:')) {
      continue;
    }
    
    if (currentResult && line && !line.startsWith('#') && !line.startsWith('**')) {
      currentResult.description = currentResult.description 
        ? currentResult.description + ' ' + line 
        : line;
    }
  }
  
  if (currentResult && currentResult.title) {
    results.push(currentResult as WebSearchResult);
  }

  if (results.length === 0) {
    return (
      <div className="tool-content-websearch">
        <LightweightCodeViewer
          content={content}
          language="text"
          maxHeight={200}
          minHeight={0}
          showLineNumbers={false}
          noBorder
        />
      </div>
    );
  }

  return (
    <div className="tool-content-websearch">
      <div className="px-2 py-1 text-[10px] text-muted-foreground border-b border-border/30">
        {queryText && <span>Searched: <span className="font-medium text-foreground">"{queryText}"</span> - </span>}
        {resultCount} result{resultCount !== '1' ? 's' : ''}
      </div>
      <div className="divide-y divide-border/20">
        {results.map((result, idx) => (
          <div key={idx} className="px-2 py-1.5">
            <div className="text-[11px] font-medium">
              {result.url ? (
                <a 
                  href={result.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="hover:underline text-info"
                  onClick={(e) => e.stopPropagation()}
                >
                  {result.title}
                </a>
              ) : (
                result.title
              )}
            </div>
            {result.url && (
              <div className="text-[9px] text-muted-foreground truncate">
                {result.url}
              </div>
            )}
            {result.description && (
              <div className="text-[10px] text-muted-foreground mt-0.5 line-clamp-2">
                {result.description}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

export const ReadToolRenderer = memo(ReadToolRendererComponent);
