import { useEffect, useId, useMemo, useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import { getFilePreviewBlob } from '../../api/fileSystem';
import { useFileOpener } from '../../lib/fileOpener';
import { openLink } from '../../lib/open-link';

export interface MarkdownFilePreviewProps {
  content: string;
  filePath: string;
  worktreeId?: string;
  worktreePath?: string;
}

// Document URLs are not chat text: no bare-image rewriting or code-path detection.
function safeUrl(url: string, image = false): string {
  // Reject URL parser whitespace/control normalization and Windows/UNC ambiguity.
  // eslint-disable-next-line no-control-regex
  if (!url || /[\u0000-\u0020\u007f\\]/.test(url) || url.startsWith('//')) return '';
  const scheme = /^[a-z][a-z\d+.-]*:/i.exec(url)?.[0].toLowerCase();
  return !scheme || ['http:', 'https:', ...(image ? [] : ['mailto:'])].includes(scheme) ? url : '';
}

function localPath(url: string, filePath: string, worktreePath?: string): string | undefined {
  // A document is untrusted input: never infer its authority from its own path.
  // Without a known worktree root, local links and images fail closed.
  if (!worktreePath?.startsWith('/')) return undefined;
  const root = worktreePath.replace(/\/+$/, '') || '/';
  const withinRoot = (path: string) => root === '/' || path === root || path.startsWith(`${root}/`);
  const documentPath = filePath.startsWith('/') ? filePath : `${root}/${filePath}`;
  if (!withinRoot(documentPath)) return undefined;
  try {
    const path = decodeURIComponent(url.split(/[?#]/)[0]);
    // Decoding must not smuggle control characters into the daemon path.
    // eslint-disable-next-line no-control-regex
    if (!path || /[\u0000-\u001f\u007f\\]/.test(path)) return undefined;
    const joined = path.startsWith('/') ? path : `${documentPath.slice(0, documentPath.lastIndexOf('/') + 1)}${path}`;
    const parts: string[] = [];
    for (const part of joined.split('/')) {
      if (!part || part === '.') continue;
      if (part === '..' && parts.length && parts.at(-1) !== '..') parts.pop();
      else if (part === '..' && joined.startsWith('/')) continue;
      else parts.push(part);
    }
    const resolved = `/${parts.join('/')}`;
    return withinRoot(resolved) ? resolved : undefined;
  } catch {
    return undefined;
  }
}

type HeadingNode = { type: string; tagName?: string; value?: string; properties?: Record<string, unknown>; children?: HeadingNode[] };
function headingText(node: HeadingNode): string {
  return node.value ?? node.children?.map(headingText).join('') ?? '';
}

function DocumentImage({ src, alt, title, filePath, worktreeId, worktreePath }: {
  src?: string; alt?: string; title?: string; filePath: string; worktreeId?: string; worktreePath?: string;
}) {
  const external = !!src && /^https?:/i.test(src);
  const path = src && !external ? localPath(src, filePath, worktreePath) : undefined;
  const [loaded, setLoaded] = useState<{ path: string; worktreeId?: string; url: string }>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let disposed = false;
    let objectUrl: string | undefined;
    setFailed(false);
    setLoaded(undefined);
    if (path && worktreeId) {
      void getFilePreviewBlob(path, worktreeId).then(blob => {
        if (disposed) return;
        objectUrl = URL.createObjectURL(blob);
        setLoaded({ path, worktreeId, url: objectUrl });
      }).catch(() => { if (!disposed) setFailed(true); });
    }
    return () => {
      disposed = true;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [path, worktreeId, src]);
  const url = external ? src : loaded?.path === path && loaded?.worktreeId === worktreeId ? loaded?.url : undefined;
  if (failed || !src || (!external && (!path || !worktreeId))) {
    return <span className="text-muted-foreground" role="img" aria-label={alt || 'Image unavailable'}>{alt || 'Image'} (unavailable)</span>;
  }
  if (!url) return <span className="text-muted-foreground">{alt || 'Image'} (loading…)</span>;
  return <img src={url} alt={alt ?? ''} title={title} loading="lazy" referrerPolicy="no-referrer" onError={() => setFailed(true)} className="h-auto max-w-full rounded" />;
}

/** Read-only document semantics, deliberately independent of the chat renderer. */
export function MarkdownFilePreview({ content, filePath, worktreeId, worktreePath }: MarkdownFilePreviewProps) {
  const openFile = useFileOpener();
  const root = useRef<HTMLElement>(null);
  const prefix = useId();
  const [linkError, setLinkError] = useState(false);
  const headings = useMemo(() => () => (tree: HeadingNode) => {
    const used = new Set<string>();
    const visit = (node: HeadingNode) => {
      if (/^h[1-6]$/.test(node.tagName ?? '')) {
        const base = headingText(node).toLowerCase().replace(/[^\p{L}\p{N}\p{M}_\s-]/gu, '').replace(/\s/g, '-');
        let slug = base;
        let index = 0;
        while (used.has(slug)) slug = `${base}-${++index}`;
        used.add(slug);
        node.properties = { ...node.properties, id: `${prefix}-${slug}` };
      }
      node.children?.forEach(visit);
    };
    visit(tree);
  }, [prefix]);

  return <article ref={root} aria-label="Markdown preview" className="mx-auto w-full min-w-0 max-w-3xl px-6 py-8 text-foreground sm:px-10">
    {linkError && <p role="status" className="text-muted-foreground">Unable to open this link.</p>}
    <div className="prose prose-sm max-w-none break-words text-foreground prose-headings:font-semibold prose-headings:text-foreground prose-p:leading-relaxed prose-strong:text-foreground prose-em:text-foreground prose-a:text-primary-ink prose-blockquote:border-border prose-blockquote:text-muted-foreground prose-blockquote:font-normal prose-blockquote:not-italic prose-li:marker:text-muted-foreground prose-hr:border-border prose-code:text-foreground prose-code:before:content-none prose-code:after:content-none prose-pre:bg-background prose-pre:text-foreground prose-pre:border prose-pre:border-border/60 prose-th:text-foreground prose-th:border-border prose-td:border-border prose-tr:border-border">
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[headings, [rehypeHighlight, { detect: false }]]} skipHtml
        urlTransform={(url, key) => safeUrl(url, key === 'src')}
        components={{
          a: ({ href, children, title }) => {
            if (!href) return <span>{children}</span>;
            return <a href={href} title={title} onClick={event => {
              event.preventDefault();
              setLinkError(false);
              if (href.startsWith('#')) {
                try {
                  const id = `${prefix}-${decodeURIComponent(href.slice(1))}`;
                  const target = Array.from(root.current?.querySelectorAll('[id]') ?? []).find(element => element.id === id);
                  target?.scrollIntoView({ block: 'start' });
                } catch { setLinkError(true); }
              } else if (/^(https?:|mailto:)/i.test(href)) {
                void openLink(href, worktreeId).catch(() => setLinkError(true));
              } else {
                const path = worktreeId ? localPath(href, filePath, worktreePath) : undefined;
                if (!path) { setLinkError(true); return; }
                try { openFile({ path, fullPath: path, isAbsolute: path.startsWith('/') }, worktreeId); }
                catch { setLinkError(true); }
              }
            }}>{children}</a>;
          },
          img: ({ src, alt, title }) => <DocumentImage key={`${filePath}:${worktreeId}:${src}`} src={src} alt={alt} title={title} filePath={filePath} worktreeId={worktreeId} worktreePath={worktreePath} />,
          table: ({ children }) => <div className="overflow-x-auto"><table>{children}</table></div>,
        }}
      >{content}</ReactMarkdown>
    </div>
  </article>;
}
