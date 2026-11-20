import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import { cn } from '../../lib/utils';
import type { Components } from 'react-markdown';
import { useState, useMemo, useCallback } from 'react';
import { ImagePreviewModal } from './ImagePreviewModal';
import { isFilePath } from '../../lib/filePath';
import { isHttpUrl } from '../../lib/url';
import { FileLink } from './FileLink';
import { CodeBlock } from './CodeBlock';
import { openLink } from '../../lib/open-link';
import { CHAT_LINK_CLASS } from './LinkifiedText';

interface MarkdownRendererProps {
  content: string;
  className?: string;
  isUser?: boolean;
  isStreaming?: boolean; // Skip expensive operations during streaming
  worktreeId?: string; // Worktree context for file links
  localImages?: Record<string, string>; // Optional map of local markdown image paths to data URLs
}

interface MarkdownImageProps {
  src?: string;
  alt?: string;
  onImageClick: (url: string, alt: string) => void;
  imageErrors: Set<string>;
  setImageErrors: (errors: Set<string>) => void;
}

// One highlighter for every message. react-markdown builds its processor on
// each render, which runs a plugin's attacher each time, and rehype-highlight's
// attacher builds a lowlight and registers its ~37 grammars: ~16ms per
// rendered message, paid again by every re-render (a chat switch renders a
// screenful of them). The transformer it returns holds no per-document state,
// so one can serve them all.
const highlightTransformer = rehypeHighlight();
const sharedRehypeHighlight = () => highlightTransformer;

/**
 * An inline code span: mono at the surrounding size, on a faint ink wash, with
 * no border (a bordered chip per span made prose read as a form).
 */
const INLINE_CODE_CLASS = "rounded bg-foreground/[0.06] px-1 py-px font-mono text-[0.875em] font-normal text-foreground";
/** A file path: mono with no box; it opens in the right panel. */
const FILE_PATH_CLASS = "font-mono text-[0.875em] decoration-border hover:underline";

const IMAGE_URL_PATTERN = /\bhttps?:\/\/[^\s<>]+?\.(?:jpg|jpeg|png|gif|webp|svg|bmp|ico)(?:\?[^\s<>]*)?\b/gi;

function convertPlainImageUrlsToMarkdown(content: string): string {
  const alreadyMarkedImages = new Set<string>();
  
  const markdownImagePattern = /!\[([^\]]*)\]\(([^)]+)\)/g;
  let match;
  while ((match = markdownImagePattern.exec(content)) !== null) {
    alreadyMarkedImages.add(match[2]);
  }
  
  return content.replace(IMAGE_URL_PATTERN, (url) => {
    if (alreadyMarkedImages.has(url)) {
      return url;
    }
    
    const filename = url.split('/').pop()?.split('?')[0] || 'Image';
    return `![${filename}](${url})`;
  });
}

function MarkdownImage({ src, alt, onImageClick, imageErrors, setImageErrors, ...props }: MarkdownImageProps) {
  const [isLoading, setIsLoading] = useState(true);
  
  if (!src) return null;
  
  const hasError = imageErrors.has(src);

  return (
    <div className="my-3 rounded-xl overflow-hidden border border-border inline-block max-w-full">
      {hasError ? (
        <div className="flex items-center justify-center p-8 bg-muted text-muted-foreground text-sm">
          <div className="text-center">
            <div className="mb-2">Failed to load image</div>
            <div className="text-xs opacity-70">{alt || src}</div>
          </div>
        </div>
      ) : (
        <>
          {isLoading && (
            <div className="flex items-center justify-center p-8 bg-muted animate-pulse">
              <div className="text-sm text-muted-foreground">Loading image...</div>
            </div>
          )}
          <img
            src={src}
            alt={alt || 'Image'}
            className="max-h-[480px] max-w-full h-auto cursor-pointer hover:opacity-90 transition-opacity"
            style={{ display: isLoading ? 'none' : 'block' }}
            onClick={() => onImageClick(src, alt || 'Image')}
            onLoad={() => setIsLoading(false)}
            onError={() => {
              setIsLoading(false);
              setImageErrors(new Set(imageErrors).add(src));
            }}
            title={alt ? `${alt} - Click to view full size` : 'Click to view full size'}
            {...props}
          />
        </>
      )}
    </div>
  );
}

function createMarkdownComponents(
  onImageClick: (url: string, alt: string) => void,
  imageErrors: Set<string>,
  setImageErrors: (errors: Set<string>) => void,
  worktreeId?: string,
  onLinkClick?: (url: string) => void,
  localImages?: Record<string, string>
): Components {
  return {
    img: (props) => {
      const rawSrc = typeof props.src === 'string' ? props.src : '';
      const normalizedSrc = rawSrc.replace(/\\/g, '/').replace(/^\.\//, '').replace(/^\//, '');
      const localResolvedSrc = localImages?.[rawSrc] || localImages?.[normalizedSrc] || localImages?.[normalizedSrc.split('/').pop() || ''];
      const resolvedSrc = localResolvedSrc || rawSrc;

      return (
        <MarkdownImage
          {...props}
          src={resolvedSrc}
          onImageClick={onImageClick}
          imageErrors={imageErrors}
          setImageErrors={setImageErrors}
        />
      );
    },

    code: ({ className, children, ...props }) => {
      const match = /language-(\w+)/.exec(className || '');
      const language = match ? match[1] : '';

      const isInline = !className || !className.includes('language-');

      if (isInline) {
        // Check if the inline code is a file path
        const text = String(children);
        if (isFilePath(text)) {
          return (
            <FileLink path={text} worktreeId={worktreeId} inline prose className={FILE_PATH_CLASS} />
          );
        }

        // Markdown exempts code spans from autolinking, so a URL the model wrapped
        // in backticks arrives here as literal text. It stays styled as code —
        // keeping the code foreground rather than the link color, since the author
        // marked it up as something to read, not somewhere to go — but hover
        // underline and the pointer make it clear it is still clickable.
        if (isHttpUrl(text)) {
          const url = text.trim();
          return (
            <a
              href={url}
              className={cn(INLINE_CODE_CLASS, "no-underline hover:underline cursor-pointer")}
              onClick={(e) => {
                if (onLinkClick) {
                  e.preventDefault();
                  onLinkClick(url);
                }
              }}
            >
              {children}
            </a>
          );
        }
        
        return (
          <code className={cn(INLINE_CODE_CLASS, "whitespace-pre-wrap break-words break-all")} {...props}>
            {children}
          </code>
        );
      }

      // Fenced code block - use CodeBlock component with copy button
      return (
        <CodeBlock language={language}>
          {children}
        </CodeBlock>
      );
    },

    blockquote: ({ children, ...props }) => (
      <blockquote
        className="my-3 border-l-2 border-border pl-4 text-muted-foreground"
        {...props}
      >
        {children}
      </blockquote>
    ),

    table: ({ children, ...props }) => (
      <div className="overflow-x-auto my-4">
        <table className="w-full text-ui" {...props}>
          {children}
        </table>
      </div>
    ),

    thead: ({ children, ...props }) => (
      <thead {...props}>
        {children}
      </thead>
    ),

    tbody: ({ children, ...props }) => (
      <tbody {...props}>
        {children}
      </tbody>
    ),

    tr: ({ children, ...props }) => (
      <tr {...props}>
        {children}
      </tr>
    ),

    th: ({ children, ...props }) => (
      <th className="border-b border-border pb-2 pl-0 pr-4 text-left text-meta font-medium text-muted-foreground last:pr-0" {...props}>
        {children}
      </th>
    ),

    td: ({ children, ...props }) => (
      <td className="border-b border-border-subtle py-2 pl-0 pr-4 text-foreground last:pr-0" {...props}>
        {children}
      </td>
    ),

    h1: ({ children, ...props }) => (
      <h1 className="text-section text-foreground mt-6 mb-2 first:mt-0" {...props}>
        {children}
      </h1>
    ),
    h2: ({ children, ...props }) => (
      <h2 className="text-section text-foreground mt-6 mb-2 first:mt-0" {...props}>
        {children}
      </h2>
    ),
    h3: ({ children, ...props }) => (
      <h3 className="text-ui font-semibold text-foreground mt-5 mb-1.5 first:mt-0" {...props}>
        {children}
      </h3>
    ),
    h4: ({ children, ...props }) => (
      <h4 className="text-ui font-semibold text-foreground mt-5 mb-1.5 first:mt-0" {...props}>
        {children}
      </h4>
    ),
    h5: ({ children, ...props }) => (
      <h5 className="text-ui font-semibold text-foreground mt-5 mb-1.5 first:mt-0" {...props}>
        {children}
      </h5>
    ),
    h6: ({ children, ...props }) => (
      <h6 className="text-ui font-semibold text-foreground mt-5 mb-1.5 first:mt-0" {...props}>
        {children}
      </h6>
    ),

    p: ({ children, ...props }) => (
      <p className="text-foreground leading-[var(--chat-prose-leading,1.625)] my-0 [&+&]:mt-3" {...props}>
        {children}
      </p>
    ),

    ul: ({ children, ...props }) => (
      <ul className="my-3 list-disc space-y-1.5 pl-5" {...props}>
        {children}
      </ul>
    ),
    ol: ({ children, ...props }) => (
      <ol className="my-3 list-decimal space-y-1.5 pl-5" {...props}>
        {children}
      </ol>
    ),
    li: ({ children, ...props }) => (
      <li className="text-foreground marker:text-muted-foreground" {...props}>
        {children}
      </li>
    ),

    hr: ({ ...props }) => (
      <hr className="my-6 border-border-subtle" {...props} />
    ),

    a: ({ children, href, ...props }) => (
      <a
        href={href}
        className={CHAT_LINK_CLASS}
        onClick={(e) => {
          if (href && onLinkClick) {
            e.preventDefault();
            onLinkClick(href);
          }
        }}
        {...props}
      >
        {children}
      </a>
    ),

    strong: ({ children, ...props }) => (
      <strong className="text-foreground font-semibold" {...props}>
        {children}
      </strong>
    ),
    em: ({ children, ...props }) => (
      <em className="text-foreground italic" {...props}>
        {children}
      </em>
    ),
  };
}

export function MarkdownRenderer({
  content,
  className,
  isUser = false,
  isStreaming = false,
  worktreeId,
  localImages,
}: MarkdownRendererProps) {
  const [previewImage, setPreviewImage] = useState<{ url: string; filename: string } | null>(null);
  const [imageErrors, setImageErrors] = useState<Set<string>>(new Set());

  const handleImageClick = useCallback((url: string, alt: string) => {
    setPreviewImage({ url, filename: alt || 'Image' });
  }, []);

  const handleLinkClick = useCallback((url: string) => {
    // Only handle http(s) links
    if (url.startsWith('http://') || url.startsWith('https://')) {
      openLink(url, worktreeId);
    }
  }, [worktreeId]);

  const processedContent = useMemo(() => {
    return convertPlainImageUrlsToMarkdown(content);
  }, [content]);

  // Memoized, and that is load-bearing for text selection, not just speed.
  // Every entry is a component FUNCTION, and react-markdown uses them as the
  // element types for <p>, <li>, <code>, … A fresh object per render means
  // fresh types per render, so React unmounted and rebuilt the message's
  // entire DOM on every re-render of an already-settled message. Any text
  // selection anchored in it lost its anchor node mid-drag — the highlight
  // "jumped" or collapsed onto unrelated text while a reply streamed, because
  // streaming re-renders the visible messages many times a second.
  const markdownComponents = useMemo(
    () =>
      createMarkdownComponents(
        handleImageClick,
        imageErrors,
        setImageErrors,
        worktreeId,
        handleLinkClick,
        localImages,
      ),
    [handleImageClick, imageErrors, worktreeId, handleLinkClick, localImages],
  );
  
  // Skip expensive syntax highlighting during streaming for better performance
  // Only use basic GFM rendering, add highlighting when streaming completes
  const rehypePlugins = useMemo(() => {
    return isStreaming ? [] : [sharedRehypeHighlight];
  }, [isStreaming]);

  return (
    <>
    <div className={cn(
      // min-w-0 + break-words: a long unbreakable token (a URL, a bold
      // identifier) wraps at the column edge instead of running past it and
      // scrolling the whole transcript sideways. break-words (overflow-wrap:
      // break-word), not `anywhere`, so a table keeps its natural column
      // widths and scrolls inside its own container.
      // The element rules are the components above; `prose` only supplies
      // what they leave alone. No `prose-sm`: the size is the caller's
      // (`text-copy` for an answer), `text-sm` until it says otherwise.
      "prose max-w-none min-w-0 break-words text-sm text-foreground",
      // Paragraph leading is the type role's unless the caller sets
      // --chat-prose-leading (the mobile transcript, ChatMessage).
      "prose-p:leading-[var(--chat-prose-leading,1.625)]",
      "prose-strong:text-foreground prose-strong:font-semibold",
      "prose-em:text-foreground",
      "prose-li:text-foreground prose-li:my-0",
      "prose-blockquote:font-normal prose-blockquote:not-italic",
      // The cells draw the table's rules (border-border-subtle), not prose.
      "prose-thead:border-0 prose-tr:border-0",
      "prose-code:before:content-none prose-code:after:content-none",
      "prose-pre:my-3 prose-pre:p-0 prose-pre:bg-transparent prose-pre:border-0 prose-pre:shadow-none prose-pre:rounded-none",

      isUser && "prose-invert-0",

      className
    )}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={rehypePlugins}
        components={markdownComponents}
      >
        {processedContent}
      </ReactMarkdown>
    </div>

    {previewImage && (
      <ImagePreviewModal
        isOpen={true}
        onClose={() => setPreviewImage(null)}
        imageUrl={previewImage.url}
        filename={previewImage.filename}
      />
    )}
    </>
  );
}
