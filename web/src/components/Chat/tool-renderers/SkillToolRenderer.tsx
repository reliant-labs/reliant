/**
 * Renderer for the `skill` tool (load/list/search actions)
 * Shows skill-specific UI instead of raw JSON
 */

import { memo, useState } from 'react';
import { BookMarked, ChevronDown, ChevronRight } from 'lucide-react';
import type { ToolContentProps } from './types';
import { MarkdownRenderer } from '../MarkdownRenderer';

function SkillToolRendererComponent({ ctx }: ToolContentProps) {
  const { input, result, worktreeId } = ctx;
  const data = typeof input === 'string' ? {} : (input || {});
  const action = (data.action as string) || '';
  const skillName = (data.name as string) || (data.skill_name as string) || '';

  if (action === 'load') {
    return <LoadActionContent skillName={skillName} result={result?.content} worktreeId={worktreeId} />;
  }

  if (action === 'list') {
    return <ListActionContent result={result?.content} />;
  }

  if (action === 'search') {
    return <SearchActionContent query={(data.query as string) || ''} result={result?.content} />;
  }

  // Fallback: show result if present
  if (result?.content) {
    return (
      <div className="px-2 py-1.5 text-xs text-muted-foreground">
        {result.content}
      </div>
    );
  }

  return null;
}

function LoadActionContent({ 
  skillName, 
  result, 
  worktreeId 
}: { 
  skillName: string; 
  result?: string; 
  worktreeId?: string;
}) {
  const [showBody, setShowBody] = useState(false);

  return (
    <div className="tool-content-skill">
      {/* Skill name + action */}
      <div className="px-2 py-1.5 border-b border-border/30 bg-muted/20 flex items-center gap-2">
        <BookMarked className="w-3.5 h-3.5 text-primary flex-shrink-0" />
        <span className="text-xs font-medium text-foreground truncate">
          {skillName || 'Loading skill...'}
        </span>
        <span className="inline-flex items-center px-1.5 py-0.5 rounded-full text-3xs font-medium bg-primary/10 text-primary border border-primary/20">
          load
        </span>
      </div>

      {/* Collapsible skill body */}
      {result && (
        <div>
          <button
            onClick={() => setShowBody(!showBody)}
            className="w-full flex items-center gap-1 px-2 py-1 text-2xs text-muted-foreground hover:text-foreground hover:bg-muted/30 transition-colors"
          >
            {showBody ? (
              <ChevronDown className="w-3 h-3" />
            ) : (
              <ChevronRight className="w-3 h-3" />
            )}
            <span>Skill content</span>
          </button>
          {showBody && (
            <div className="px-2 py-1.5 border-t border-border/30 text-xs max-h-[300px] overflow-y-auto">
              <MarkdownRenderer content={result} worktreeId={worktreeId} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function ListActionContent({ result }: { result?: string }) {
  if (!result) return null;

  // Try to parse as JSON array of skill names/objects
  let skills: string[] = [];
  try {
    const parsed = JSON.parse(result);
    if (Array.isArray(parsed)) {
      skills = parsed.map((s: unknown) => {
        if (typeof s === 'string') return s;
        if (typeof s === 'object' && s !== null) {
          const obj = s as Record<string, unknown>;
          return (obj.name as string) || (obj.title as string) || JSON.stringify(s);
        }
        return String(s);
      });
    }
  } catch {
    // Not JSON — show as text
    return (
      <div className="px-2 py-1.5 text-xs text-muted-foreground whitespace-pre-wrap">
        {result}
      </div>
    );
  }

  if (skills.length === 0) {
    return (
      <div className="px-2 py-1.5 text-xs text-muted-foreground">
        No skills available
      </div>
    );
  }

  return (
    <div className="tool-content-skill-list">
      <div className="px-2 py-1 text-2xs text-muted-foreground bg-muted/30 border-b border-border/30">
        Available Skills ({skills.length})
      </div>
      <div className="px-2 py-1.5 space-y-0.5">
        {skills.map((skill, i) => (
          <div key={i} className="flex items-center gap-1.5 text-xs">
            <BookMarked className="w-3 h-3 text-muted-foreground flex-shrink-0" />
            <span className="text-foreground">{skill}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function SearchActionContent({ query, result }: { query: string; result?: string }) {
  if (!result) return null;

  return (
    <div className="tool-content-skill-search">
      {query && (
        <div className="px-2 py-1 text-2xs text-muted-foreground bg-muted/30 border-b border-border/30">
          Search: &quot;{query}&quot;
        </div>
      )}
      <div className="px-2 py-1.5 text-xs text-muted-foreground whitespace-pre-wrap">
        {result}
      </div>
    </div>
  );
}

export const SkillToolRenderer = memo(SkillToolRendererComponent);
