/**
 * Tool Classification and Formatting Utilities
 * 
 * Provides standardized interfaces and formatters for displaying tool calls
 * in the UI with consistent parameter extraction and formatting.
 */

/**
 * Tool categories for UI grouping and display logic
 */
export const ToolCategory = {
  /** Read-only tools that explore/search (view, grep, websearch, diagnostics, glob, ls) */
  READ_ONLY: 'read_only',
  /** Tools that modify files or system state (write, edit, patch, shell with write operations) */
  WRITE: 'write',
  /** Planning and task management tools (create_plan, add_task, update_task) */
  PLANNING: 'planning',
  /** Execution tools (shell, run_command) */
  EXECUTION: 'execution',
} as const;
export type ToolCategory = typeof ToolCategory[keyof typeof ToolCategory];

/**
 * Tool input can be either a structured object or a raw string
 */
export type ToolInput = Record<string, unknown> | string;

/**
 * Formatted tool parameters for display
 */
export interface FormattedToolParams {
  /** Short summary for collapsed view (e.g., "file.ts") */
  summary: string;
  /** Full text representation for tooltips */
  fullText: string;
  /** Structured data for rich rendering (e.g., file paths for links) */
  structured?: {
    filePaths?: string[]; // Can include :line format for FileLink
    query?: string;
    command?: string;
    description?: string;
    lineRange?: { start: number; end?: number };
    [key: string]: unknown;
  };
}

/**
 * Tool formatter function signature
 */
export type ToolFormatter = (input: ToolInput) => FormattedToolParams;

/**
 * Map of tool names to their categories
 */
export const TOOL_CATEGORIES: Record<string, ToolCategory> = {
  // Read-only tools
  view: ToolCategory.READ_ONLY,
  read: ToolCategory.READ_ONLY,
  read_files: ToolCategory.READ_ONLY,
  grep: ToolCategory.READ_ONLY,
  glob: ToolCategory.READ_ONLY,
  ls: ToolCategory.READ_ONLY,
  find_files: ToolCategory.READ_ONLY,
  websearch: ToolCategory.READ_ONLY,
  fetch: ToolCategory.READ_ONLY,
  diagnostics: ToolCategory.READ_ONLY,
  
  // Write tools
  write: ToolCategory.WRITE,
  edit: ToolCategory.WRITE,
  patch: ToolCategory.WRITE,
  find_replace: ToolCategory.WRITE,
  
  // Planning tools
  create_plan: ToolCategory.PLANNING,
  update_plan: ToolCategory.PLANNING,
  add_task: ToolCategory.PLANNING,
  update_task: ToolCategory.PLANNING,
  list_tasks: ToolCategory.PLANNING,
  get_plan: ToolCategory.PLANNING,
  
  // Execution tools
  bash: ToolCategory.EXECUTION,
  powershell: ToolCategory.EXECUTION,
  run_command: ToolCategory.EXECUTION,
};

/**
 * Get the category for a tool
 */
export function getToolCategory(toolName: string): ToolCategory {
  return TOOL_CATEGORIES[toolName.toLowerCase()] || ToolCategory.EXECUTION;
}

/**
 * Check if a tool is read-only (should be grouped/collapsed)
 */
export function isReadOnlyTool(toolName: string): boolean {
  return getToolCategory(toolName) === ToolCategory.READ_ONLY;
}

/**
 * Extract file path from various parameter formats
 */
function extractFilePath(input: ToolInput): string | undefined {
  if (typeof input !== 'object' || input === null) return undefined;
  
  return (
    (input.file_path as string) ||
    (input.FilePath as string) ||
    (input.path as string) ||
    (input.Path as string)
  );
}

/**
 * Format file path for display (show only filename, or relative path if short)
 */
function formatFilePath(path: string, maxLength: number = 40): string {
  if (path.length <= maxLength) return path;
  
  const parts = path.split('/');
  const filename = parts[parts.length - 1];
  
  if (filename.length <= maxLength) return filename;
  
  // Truncate in the middle
  const half = Math.floor(maxLength / 2) - 2;
  return `${filename.slice(0, half)}...${filename.slice(-half)}`;
}

/**
 * Format view/read tool parameters
 */
export const formatViewParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'view()' };
  }
  
  const filePath = extractFilePath(input);
  const offset = input.offset as number | undefined;
  const limit = input.limit as number | undefined;
  
  if (!filePath) {
    return { summary: '', fullText: 'view()' };
  }
  
  const fileName = formatFilePath(filePath);
  let summary = fileName;
  let fullText = `view(${filePath})`;
  
  // Build file path with line range for FileLink
  let filePathWithLine = filePath;
  
  // Add line range if specified
  if (offset !== undefined || limit !== undefined) {
    const start = offset || 1;
    const end = limit !== undefined ? start + limit - 1 : undefined;
    summary = `${fileName} L:${start}-${end || '...'}`;
    fullText = `view(${filePath} L:${start}-${end || '...'})`;
    // FileLink expects path:line:endLine format for ranges
    filePathWithLine = end ? `${filePath}:${start}:${end}` : `${filePath}:${start}`;
  }
  
  return {
    summary,
    fullText,
    structured: { 
      filePaths: [filePathWithLine],
      lineRange: offset !== undefined || limit !== undefined ? { 
        start: offset || 1, 
        end: limit !== undefined ? (offset || 1) + limit - 1 : undefined 
      } : undefined
    },
  };
};

/**
 * Format grep tool parameters
 */
export const formatGrepParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'grep()' };
  }
  
  const pattern = input.pattern as string | undefined;
  const path = input.path as string | undefined;
  const glob = input.glob as string | undefined;
  const type = input.type as string | undefined;
  
  if (!pattern) {
    return { summary: '', fullText: 'grep()' };
  }
  
  // Truncate pattern if too long
  const truncatedPattern = pattern.length > 30 
    ? `${pattern.slice(0, 27)}...`
    : pattern;
  
  let summary = `"${truncatedPattern}"`;
  let fullText = `grep(pattern: "${pattern}")`;
  
  // Add location info
  if (glob) {
    summary += ` in ${glob}`;
    fullText = `grep(pattern: "${pattern}", glob: "${glob}")`;
  } else if (type) {
    summary += ` in *.${type}`;
    fullText = `grep(pattern: "${pattern}", type: ${type})`;
  } else if (path) {
    const pathStr = formatFilePath(path);
    summary += ` in ${pathStr}`;
    fullText = `grep(pattern: "${pattern}", path: "${path}")`;
  }
  
  return {
    summary,
    fullText,
    structured: { query: pattern, filePaths: path ? [path] : undefined },
  };
};

/**
 * Format websearch tool parameters
 */
export const formatWebsearchParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'websearch()' };
  }
  
  const query = input.query as string | undefined;
  const count = input.count as number | undefined;
  
  if (!query) {
    return { summary: '', fullText: 'websearch()' };
  }
  
  // Truncate query if too long
  const truncatedQuery = query.length > 40
    ? `${query.slice(0, 37)}...`
    : query;
  
  const summary = `"${truncatedQuery}"`;
  let fullText = `websearch(query: "${query}")`;
  
  if (count) {
    fullText += `, count: ${count}`;
  }
  
  return {
    summary,
    fullText,
    structured: { query },
  };
};

/**
 * Format shell tool parameters
 */
export const formatShellParams: ToolFormatter = (input) => {
  let command: string | undefined;
  let description: string | undefined;

  if (typeof input === 'string') {
    command = input;
  } else if (typeof input === 'object' && input !== null) {
    command = (input.command as string) || (input.Command as string);
    const raw = (input.description as string) || (input.Description as string);
    description = typeof raw === 'string' && raw.trim() ? raw.trim() : undefined;
  }

  if (!command) {
    return { summary: '', fullText: 'shell()' };
  }

  // The model-authored description says what the command does in prose, which
  // reads better in a collapsed row than the command line itself. The command
  // is still the fullText/structured value so expanded views and copy are exact.
  return {
    summary: description ?? command,
    fullText: `shell(${command})`,
    structured: { command, description },
  };
};

/**
 * Format diagnostics tool parameters
 */
export const formatDiagnosticsParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: 'project', fullText: 'diagnostics()' };
  }
  
  const filePath = extractFilePath(input);
  
  if (!filePath) {
    return { summary: 'project', fullText: 'diagnostics(project)' };
  }
  
  const fileName = formatFilePath(filePath);
  
  return {
    summary: fileName,
    fullText: `diagnostics(${filePath})`,
    structured: { filePaths: [filePath] },
  };
};

/**
 * Format glob tool parameters
 */
export const formatGlobParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'glob()' };
  }
  
  const pattern = input.pattern as string | undefined;
  const path = input.path as string | undefined;
  
  if (!pattern) {
    return { summary: '', fullText: 'glob()' };
  }
  
  let summary = pattern;
  let fullText = `glob(pattern: "${pattern}")`;
  
  if (path) {
    const pathStr = formatFilePath(path);
    summary = `${pattern} in ${pathStr}`;
    fullText = `glob(pattern: "${pattern}", path: "${path}")`;
  }
  
  return {
    summary,
    fullText,
    structured: { query: pattern },
  };
};

/**
 * Format fetch tool parameters
 */
export const formatFetchParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'fetch()' };
  }
  
  const url = input.url as string | undefined;
  const format = input.format as string | undefined;
  
  if (!url) {
    return { summary: '', fullText: 'fetch()' };
  }
  
  // Extract domain from URL for summary
  try {
    const urlObj = new URL(url);
    const domain = urlObj.hostname.replace('www.', '');
    const summary = domain.length > 30 ? `${domain.slice(0, 27)}...` : domain;
    
    let fullText = `fetch(url: "${url}")`;
    if (format) {
      fullText += `, format: ${format}`;
    }
    
    return {
      summary,
      fullText,
      structured: { query: url },
    };
  } catch {
    // Invalid URL, just truncate
    const truncatedUrl = url.length > 40 ? `${url.slice(0, 37)}...` : url;
    return {
      summary: truncatedUrl,
      fullText: `fetch(url: "${url}")`,
      structured: { query: url },
    };
  }
};

/**
 * Format ls tool parameters
 */
export const formatLsParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '.', fullText: 'ls()' };
  }
  
  const path = input.path as string | undefined;
  
  if (!path) {
    return { summary: '.', fullText: 'ls(current directory)' };
  }
  
  const pathStr = formatFilePath(path);
  
  return {
    summary: pathStr,
    fullText: `ls(${path})`,
    structured: { filePaths: [path] },
  };
};

/**
 * Format update_task tool parameters
 */
export const formatUpdateTaskParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'update_task()' };
  }
  
  const taskId = input.task_id as string | undefined;
  const status = input.status as string | undefined;
  const description = input.description as string | undefined;
  
  if (!taskId) {
    return { summary: '', fullText: 'update_task()' };
  }
  
  // Truncate task ID for display (first 7 chars like git)
  const shortId = taskId.length > 7 ? taskId.slice(0, 7) : taskId;
  
  let summary = shortId;
  if (status) {
    summary += ` → ${status}`;
  }
  
  let fullText = `update_task(${taskId}`;
  if (status) {
    fullText += `, status: ${status}`;
  }
  if (description) {
    fullText += `, description: "${description.slice(0, 50)}${description.length > 50 ? '...' : ''}"`;
  }
  fullText += ')';
  
  return {
    summary,
    fullText,
    structured: { 
      taskId, 
      status, 
      description,
      shortId,
    },
  };
};

/**
 * Format add_task tool parameters
 */
export const formatAddTaskParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'add_task()' };
  }
  
  const title = input.title as string | undefined;
  
  if (!title) {
    return { summary: '', fullText: 'add_task()' };
  }
  
  // Truncate title for display
  const truncatedTitle = title.length > 40 ? title.slice(0, 37) + '...' : title;
  
  return {
    summary: truncatedTitle,
    fullText: `add_task("${title}")`,
    structured: { title },
  };
};

/**
 * Format write tool parameters
 */
export const formatWriteParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'write()' };
  }
  
  const filePath = extractFilePath(input);
  
  if (!filePath) {
    return { summary: '', fullText: 'write()' };
  }
  
  const fileName = formatFilePath(filePath);
  
  return {
    summary: fileName,
    fullText: `write(${filePath})`,
    structured: { filePaths: [filePath] },
  };
};

/**
 * Format edit tool parameters (supports multi-edit)
 */
export const formatEditParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'edit()' };
  }

  // Check for MultiEdit format (edits array) - handle both array and stringified array
  let edits = input.edits as Array<Record<string, unknown>> | string | undefined;

  // If edits is a string, try to parse it
  if (typeof edits === 'string') {
    try {
      edits = JSON.parse(edits) as Array<Record<string, unknown>>;
    } catch {
      edits = undefined;
    }
  }

  if (edits && Array.isArray(edits) && edits.length > 0) {
    const filePaths = edits
      .map(edit => (edit.file_path || edit.FilePath) as string)
      .filter(Boolean);
    
    // Get unique file paths
    const uniquePaths = [...new Set(filePaths)];
    
    if (uniquePaths.length === 0) {
      return { summary: '', fullText: 'edit()' };
    }
    
    if (uniquePaths.length === 1) {
      const fileName = formatFilePath(uniquePaths[0]);
      return {
        summary: fileName,
        fullText: `edit(${uniquePaths[0]})`,
        structured: { filePaths: uniquePaths },
      };
    }
    
    // Multiple files
    const fileNames = uniquePaths.map(p => formatFilePath(p, 20));
    const summary = fileNames.length <= 2 
      ? fileNames.join(', ')
      : `${fileNames[0]} +${uniquePaths.length - 1} more`;
    
    return {
      summary,
      fullText: `edit(${uniquePaths.join(', ')})`,
      structured: { filePaths: uniquePaths },
    };
  }
  
  // Single edit format
  const filePath = extractFilePath(input);
  
  if (!filePath) {
    return { summary: '', fullText: 'edit()' };
  }
  
  const fileName = formatFilePath(filePath);
  
  return {
    summary: fileName,
    fullText: `edit(${filePath})`,
    structured: { filePaths: [filePath] },
  };
};

/**
 * Format patch tool parameters
 */
export const formatPatchParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: 'patch()' };
  }
  
  const patchText = input.patch_text as string | undefined;
  
  if (!patchText) {
    return { summary: '', fullText: 'patch()' };
  }
  
  // Extract file paths from patch text (*** Update File: /path or *** Add File: /path)
  const fileMatches = patchText.match(/\*\*\*\s+(?:Update|Add|Delete)\s+File:\s+([^\n]+)/g);
  
  if (fileMatches && fileMatches.length > 0) {
    const filePaths = fileMatches.map(match => {
      const pathMatch = match.match(/:\s+(.+)$/);
      return pathMatch ? pathMatch[1].trim() : '';
    }).filter(Boolean);
    
    if (filePaths.length === 1) {
      const fileName = formatFilePath(filePaths[0]);
      return {
        summary: fileName,
        fullText: `patch(${filePaths[0]})`,
        structured: { filePaths },
      };
    }
    
    if (filePaths.length > 1) {
      const fileNames = filePaths.map(p => formatFilePath(p, 20));
      const summary = fileNames.length <= 2
        ? fileNames.join(', ')
        : `${fileNames[0]} +${filePaths.length - 1} more`;
      
      return {
        summary,
        fullText: `patch(${filePaths.join(', ')})`,
        structured: { filePaths },
      };
    }
  }
  
  // Fallback - just show patch text length
  return {
    summary: `${patchText.length} chars`,
    fullText: 'patch()',
    structured: {},
  };
};

/**
 * Format find_replace tool parameters
 */
export const formatFindReplaceParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) return { summary: '', fullText: 'find_replace()' };
  const findPattern = input.find_pattern as string | undefined;
  const fileGlob = input.file_glob as string | undefined;
  if (!findPattern) return { summary: '', fullText: 'find_replace()' };
  const truncPattern = findPattern.length > 30 ? findPattern.slice(0, 27) + '...' : findPattern;
  let summary = `"${truncPattern}"`;
  if (fileGlob) summary += ` in ${fileGlob}`;
  return { summary, fullText: `find_replace("${findPattern}"${fileGlob ? `, glob: "${fileGlob}"` : ''})`, structured: { query: findPattern } };
};

/**
 * Format load_tool parameters
 */
export const formatLoadToolParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) return { summary: '', fullText: 'load_tool()' };
  const name = input.name as string | undefined;
  const query = input.query as string | undefined;
  if (name) return { summary: name, fullText: `load_tool(${name})`, structured: {} };
  if (query) return { summary: `"${query}"`, fullText: `load_tool(query: "${query}")`, structured: { query } };
  return { summary: '', fullText: 'load_tool()' };
};

/**
 * Format skill tool parameters
 */
export const formatSkillParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) return { summary: '', fullText: 'skill()' };
  const action = input.action as string | undefined;
  const path = input.path as string | undefined;
  const query = input.query as string | undefined;
  if (action === 'load' && path) return { summary: `load: ${path}`, fullText: `skill(load: ${path})`, structured: {} };
  if (action === 'search' && query) return { summary: `search: "${query}"`, fullText: `skill(search: "${query}")`, structured: { query } };
  if (action === 'list') return { summary: 'list', fullText: 'skill(list)', structured: {} };
  if (action) return { summary: action, fullText: `skill(${action})`, structured: {} };
  return { summary: '', fullText: 'skill()' };
};

/**
 * Format spawn tool parameters
 */
export const formatSpawnParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) return { summary: '', fullText: 'spawn()' };
  const preset = input.preset as string | undefined;
  const title = input.title as string | undefined;
  if (preset && title) {
    const truncTitle = title.length > 35 ? title.slice(0, 32) + '...' : title;
    return { summary: `${preset}: "${truncTitle}"`, fullText: `spawn(${preset}: "${title}")`, structured: {} };
  }
  if (preset) return { summary: preset, fullText: `spawn(${preset})`, structured: {} };
  return { summary: '', fullText: 'spawn()' };
};

/**
 * Format create_plan tool parameters
 */
export const formatCreatePlanParams: ToolFormatter = (input) => {
  if (typeof input !== 'object' || input === null) return { summary: '', fullText: 'create_plan()' };
  const title = input.title as string | undefined;
  if (!title) return { summary: '', fullText: 'create_plan()' };
  const trunc = title.length > 40 ? title.slice(0, 37) + '...' : title;
  return { summary: `"${trunc}"`, fullText: `create_plan("${title}")`, structured: {} };
};

/**
 * Format list_tasks tool parameters
 */
export const formatListTasksParams: ToolFormatter = () => {
  return { summary: '', fullText: 'list_tasks()' };
};

/**
 * Map of tool names to their formatters
 */
export const TOOL_FORMATTERS: Record<string, ToolFormatter> = {
  view: formatViewParams,
  read: formatViewParams,
  read_files: formatViewParams,
  grep: formatGrepParams,
  websearch: formatWebsearchParams,
  shell: formatShellParams,
  bash: formatShellParams,
  powershell: formatShellParams,
  run_command: formatShellParams,
  diagnostics: formatDiagnosticsParams,
  glob: formatGlobParams,
  fetch: formatFetchParams,
  ls: formatLsParams,
  find_files: formatLsParams,
  update_task: formatUpdateTaskParams,
  add_task: formatAddTaskParams,
  create_plan: formatCreatePlanParams,
  list_tasks: formatListTasksParams,
  write: formatWriteParams,
  edit: formatEditParams,
  patch: formatPatchParams,
  find_replace: formatFindReplaceParams,
  load_tool: formatLoadToolParams,
  skill: formatSkillParams,
  spawn: formatSpawnParams,
};

/**
 * Get formatter for a tool, or return undefined if no specific formatter exists.
 * Handles MCP-prefixed tool names (e.g. mcp__reliant__spawn -> spawn).
 */
export function getToolFormatter(toolName: string): ToolFormatter | undefined {
  const lower = toolName.toLowerCase();
  const formatter = TOOL_FORMATTERS[lower];
  if (formatter) return formatter;
  // Try stripping MCP prefix: mcp__server__tool -> tool
  if (lower.startsWith('mcp__')) {
    const parts = lower.split('__');
    const baseName = parts[parts.length - 1];
    return TOOL_FORMATTERS[baseName];
  }
  return undefined;
}

/**
 * Generic fallthrough formatter for tools without a specific formatter.
 * Introspects the input to show the most useful-looking param values.
 */
function formatGenericParams(toolName: string, input: ToolInput): FormattedToolParams {
  const name = toolName.startsWith('mcp__')
    ? toolName.split('__').pop() || toolName
    : toolName;

  if (typeof input !== 'object' || input === null) {
    return { summary: '', fullText: `${name}()` };
  }

  // Priority order: look for the most descriptive-sounding keys
  const priorityKeys = [
    'name', 'title', 'query', 'path', 'file_path', 'pattern',
    'url', 'command', 'action', 'preset', 'id', 'task_id',
  ];

  const parts: string[] = [];
  for (const key of priorityKeys) {
    const val = input[key];
    if (typeof val === 'string' && val.length > 0) {
      const trunc = val.length > 35 ? val.slice(0, 32) + '...' : val;
      parts.push(trunc);
      if (parts.length >= 2) break;
    }
  }

  // If nothing from priority keys, grab first string value
  if (parts.length === 0) {
    for (const val of Object.values(input)) {
      if (typeof val === 'string' && val.length > 0 && val.length < 60) {
        parts.push(val.length > 35 ? val.slice(0, 32) + '...' : val);
        break;
      }
    }
  }

  const summary = parts.join(', ');
  return { summary, fullText: summary ? `${name}(${summary})` : `${name}()`, structured: {} };
}

/**
 * Format tool parameters for display
 * Uses specific formatter if available, otherwise falls through to generic.
 */
export function formatToolParams(
  toolName: string,
  input: ToolInput
): FormattedToolParams | undefined {
  const formatter = getToolFormatter(toolName);
  if (formatter) return formatter(input);
  // Generic fallthrough — try to show something useful
  return formatGenericParams(toolName, input);
}

/**
 * Generate a human-readable summary for grouped read-only tools
 * Example: "Explored 2 files 1 search"
 */
export function generateReadOnlyToolsSummary(tools: Array<{ name: string; input: ToolInput }>): string {
  const counts: Record<string, number> = {};
  
  tools.forEach(({ name }) => {
    const nameLower = name.toLowerCase();
    
    if (nameLower === 'view' || nameLower === 'read' || nameLower === 'read_files') {
      counts.files = (counts.files || 0) + 1;
    } else if (nameLower === 'grep') {
      counts.searches = (counts.searches || 0) + 1;
    } else if (nameLower === 'websearch') {
      counts.websearches = (counts.websearches || 0) + 1;
    } else if (nameLower === 'diagnostics') {
      counts.diagnostics = (counts.diagnostics || 0) + 1;
    } else if (nameLower === 'glob' || nameLower === 'ls' || nameLower === 'find_files') {
      counts.listings = (counts.listings || 0) + 1;
    } else if (nameLower === 'fetch') {
      counts.fetches = (counts.fetches || 0) + 1;
    }
  });
  
  const parts: string[] = [];
  
  if (counts.files) {
    parts.push(`${counts.files} file${counts.files > 1 ? 's' : ''}`);
  }
  if (counts.searches) {
    parts.push(`${counts.searches} search${counts.searches > 1 ? 'es' : ''}`);
  }
  if (counts.websearches) {
    parts.push(`${counts.websearches} web search${counts.websearches > 1 ? 'es' : ''}`);
  }
  if (counts.diagnostics) {
    parts.push(`${counts.diagnostics} diagnostic${counts.diagnostics > 1 ? 's' : ''}`);
  }
  if (counts.listings) {
    parts.push(`${counts.listings} listing${counts.listings > 1 ? 's' : ''}`);
  }
  if (counts.fetches) {
    parts.push(`${counts.fetches} fetch${counts.fetches > 1 ? 'es' : ''}`);
  }
  
  if (parts.length === 0) {
    return 'Explored';
  }
  
  return `Explored ${parts.join(' ')}`;
}

// ============================================================================
// Tool List Helpers - Single source of truth for tool categorization
// Used by tool-renderers and ToolExecution component
// ============================================================================

/**
 * View-only tools that open files on click (don't render expanded content)
 */
export const VIEW_ONLY_TOOLS = ['view', 'read', 'read_files'] as const;

/**
 * Check if a tool is view-only (opens file on click, no expanded content)
 */
export function isViewOnlyTool(toolName: string): boolean {
  return VIEW_ONLY_TOOLS.includes(toolName.toLowerCase() as typeof VIEW_ONLY_TOOLS[number]);
}

/**
 * Task management tools
 */
export const TASK_TOOLS = ['update_task', 'add_task'] as const;

/**
 * Check if a tool is a task tool
 */
export function isTaskTool(toolName: string): boolean {
  return TASK_TOOLS.includes(toolName.toLowerCase() as typeof TASK_TOOLS[number]);
}

/**
 * Read tools that show results with file links
 */
export const READ_TOOLS = ['grep', 'glob', 'ls', 'find_files', 'websearch', 'fetch', 'diagnostics'] as const;

/**
 * Check if a tool is a read tool (shows results)
 */
export function isReadToolWithResults(toolName: string): boolean {
  return READ_TOOLS.includes(toolName.toLowerCase() as typeof READ_TOOLS[number]);
}

/**
 * Tools that use the shell renderer
 */
export const SHELL_TOOLS = ['bash', 'powershell', 'run_command'] as const;

/**
 * Check if a tool uses the shell renderer
 */
export function isShellTool(toolName: string): boolean {
  return SHELL_TOOLS.includes(toolName.toLowerCase() as typeof SHELL_TOOLS[number]);
}

/**
 * Tools that use the file renderer (diffs)
 */
export const FILE_TOOLS = ['write', 'edit', 'patch'] as const;

/**
 * Check if a tool uses the file renderer
 */
export function isFileTool(toolName: string): boolean {
  return FILE_TOOLS.includes(toolName.toLowerCase() as typeof FILE_TOOLS[number]);
}

/**
 * Tools that use the plan renderer
 */
export const PLAN_TOOLS = ['create_plan', 'update_task', 'add_task'] as const;

/**
 * Check if a tool uses the plan renderer
 */
export function isPlanTool(toolName: string): boolean {
  return PLAN_TOOLS.includes(toolName.toLowerCase() as typeof PLAN_TOOLS[number]);
}

/**
 * Skill tool (load/list/search skills)
 */
export const SKILL_TOOLS = ['skill'] as const;

/**
 * Check if a tool is the skill tool
 */
export function isSkillTool(toolName: string): boolean {
  return SKILL_TOOLS.includes(toolName.toLowerCase() as typeof SKILL_TOOLS[number]);
}

/**
 * Dynamic tool loading tool
 */
export const LOAD_TOOL_TOOLS = ['load_tool'] as const;

/**
 * Check if a tool is the load_tool tool
 */
export function isLoadToolTool(toolName: string): boolean {
  return LOAD_TOOL_TOOLS.includes(toolName.toLowerCase() as typeof LOAD_TOOL_TOOLS[number]);
}

/**
 * Spawn tool — creates a sub-agent and has a child workflow. Does NOT include
 * sibling tools like spawn_status / spawn_send, which are ordinary read/write
 * calls against an existing spawn: they have no child workflow, so treating
 * them as spawns strands them on a status derived from a workflow that will
 * never appear.
 */
export const SPAWN_TOOLS = ['spawn'] as const;

/**
 * Check if a tool is the spawn tool. Matches the base name exactly, after
 * stripping any MCP prefix (mcp__reliant__spawn -> spawn), so spawn_status
 * and spawn_send do not match in either their bare or MCP-prefixed form.
 */
export function isSpawnTool(toolName: string): boolean {
  const lower = toolName.toLowerCase();
  const baseName = lower.startsWith('mcp__') ? lower.split('__').pop() || lower : lower;
  return SPAWN_TOOLS.includes(baseName as typeof SPAWN_TOOLS[number]);
}