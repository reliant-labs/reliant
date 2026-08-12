import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import { cn } from '../../lib/utils';
import type { Components } from 'react-markdown';
import { useState, useMemo, useCallback } from 'react';
import { ImagePreviewModal } from '../ui/ImagePreviewModal';
import { isFilePath } from '../../lib/filePath';
import { isHttpUrl } from '../../lib/url';
import { FileLink } from './FileLink';
import { CodeBlock } from './CodeBlock';
import { openLink } from '../../lib/open-link';

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
    <div className="my-4 rounded-lg overflow-hidden border border-border inline-block max-w-full">
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
            className="max-w-full h-auto cursor-pointer hover:opacity-90 transition-opacity"
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
            <FileLink path={text} worktreeId={worktreeId} inline className="bg-muted px-1.5 py-0.5 rounded text-sm font-mono border border-border/50" />
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
              className="bg-muted px-1.5 py-0.5 rounded text-sm font-mono border border-border/50 text-foreground no-underline hover:underline cursor-pointer"
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
          <code className="bg-muted px-1.5 py-0.5 rounded text-sm font-mono border border-border/50 text-foreground whitespace-pre-wrap break-words break-all" {...props}>
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
        className="border-l-4 border-primary/40 pl-4 py-3 bg-primary/5 rounded-r-lg my-4 italic text-muted-foreground"
        {...props}
      >
        {children}
      </blockquote>
    ),

    table: ({ children, ...props }) => (
      <div className="overflow-x-auto my-4 rounded-lg border border-border">
        <table className="min-w-full divide-y divide-border" {...props}>
          {children}
        </table>
      </div>
    ),

    thead: ({ children, ...props }) => (
      <thead className="bg-muted/50" {...props}>
        {children}
      </thead>
    ),

    tbody: ({ children, ...props }) => (
      <tbody className="divide-y divide-border" {...props}>
        {children}
      </tbody>
    ),

    tr: ({ children, ...props }) => (
      <tr {...props}>
        {children}
      </tr>
    ),

    th: ({ children, ...props }) => (
      <th className="px-4 py-2.5 text-left font-semibold text-foreground text-sm" {...props}>
        {children}
      </th>
    ),

    td: ({ children, ...props }) => (
      <td className="px-4 py-2 text-foreground text-sm" {...props}>
        {children}
      </td>
    ),

    h1: ({ children, ...props }) => (
      <h1 className="text-2xl font-bold text-foreground mt-6 mb-4 first:mt-0 border-b border-border/30 pb-2" {...props}>
        {children}
      </h1>
    ),
    h2: ({ children, ...props }) => (
      <h2 className="text-xl font-semibold text-foreground mt-5 mb-3 first:mt-0" {...props}>
        {children}
      </h2>
    ),
    h3: ({ children, ...props }) => (
      <h3 className="text-lg font-semibold text-foreground mt-4 mb-2 first:mt-0" {...props}>
        {children}
      </h3>
    ),
    h4: ({ children, ...props }) => (
      <h4 className="text-base font-semibold text-foreground mt-3 mb-2 first:mt-0" {...props}>
        {children}
      </h4>
    ),
    h5: ({ children, ...props }) => (
      <h5 className="text-sm font-semibold text-foreground mt-3 mb-2 first:mt-0" {...props}>
        {children}
      </h5>
    ),
    h6: ({ children, ...props }) => (
      <h6 className="text-sm font-semibold text-foreground mt-3 mb-2 first:mt-0" {...props}>
        {children}
      </h6>
    ),

    p: ({ children, ...props }) => (
      <p className="text-foreground leading-relaxed mb-3 last:mb-0" {...props}>
        {children}
      </p>
    ),

    ul: ({ children, ...props }) => (
      <ul className="my-1 space-y-1 pl-6" {...props}>
        {children}
      </ul>
    ),
    ol: ({ children, ...props }) => (
      <ol className="my-1 space-y-1 pl-6" {...props}>
        {children}
      </ol>
    ),
    li: ({ children, ...props }) => (
      <li className="text-foreground marker:text-muted-foreground" {...props}>
        {children}
      </li>
    ),

    hr: ({ ...props }) => (
      <hr className="border-border/50 my-6" {...props} />
    ),

    a: ({ children, href, ...props }) => (
      <a
        href={href}
        // --info rather than --primary: primary is the theme accent, which is
        // mocha/teal/pink/orange depending on the color scheme, so links only
        // read as blue on some themes. --info is blue in every scheme.
        className="text-info no-underline hover:underline transition-all duration-200 cursor-pointer"
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

  const handleImageClick = (url: string, alt: string) => {
    setPreviewImage({ url, filename: alt || 'Image' });
  };

  const handleLinkClick = useCallback((url: string) => {
    // Only handle http(s) links
    if (url.startsWith('http://') || url.startsWith('https://')) {
      openLink(url, worktreeId);
    }
  }, [worktreeId]);

  const processedContent = useMemo(() => {
    return convertPlainImageUrlsToMarkdown(content);
  }, [content]);

  const markdownComponents = createMarkdownComponents(
    handleImageClick,
    imageErrors,
    setImageErrors,
    worktreeId,
    handleLinkClick,
    localImages,
  );
  
  // Skip expensive syntax highlighting during streaming for better performance
  // Only use basic GFM rendering, add highlighting when streaming completes
  const rehypePlugins = useMemo(() => {
    return isStreaming ? [] : [rehypeHighlight];
  }, [isStreaming]);

  return (
    <>
    <div className={cn(
      "prose prose-sm max-w-none",
      "prose-headings:font-semibold prose-headings:text-foreground prose-headings:mb-3 prose-headings:mt-4",
      "prose-p:text-foreground prose-p:leading-relaxed prose-p:mb-3",
      "prose-strong:text-foreground prose-strong:font-semibold",
      "prose-em:text-foreground",
      "prose-blockquote:text-muted-foreground prose-blockquote:border-l-border prose-blockquote:my-3",
      "prose-hr:border-border prose-hr:my-4",


      "prose-ul:text-foreground prose-ol:text-foreground prose-ul:my-1 prose-ol:my-1",
      "prose-li:text-foreground prose-li:marker:text-muted-foreground prose-li:mb-1",

      "prose-code:text-foreground prose-code:bg-muted prose-code:px-1.5 prose-code:py-0.5 prose-code:rounded prose-code:text-sm prose-code:border prose-code:border-border/50",
      "prose-code:whitespace-pre-wrap prose-code:break-words prose-code:break-all",
      "prose-code:before:content-none prose-code:after:content-none",

      "prose-pre:my-4 prose-pre:p-0 prose-pre:bg-transparent prose-pre:border-0 prose-pre:shadow-none prose-pre:rounded-none",
      "prose-pre:text-sm prose-pre:leading-relaxed prose-pre:whitespace-pre-wrap prose-pre:break-words",

      "prose-table:text-foreground prose-thead:border-b prose-thead:border-border prose-table:my-3",
      "prose-th:text-foreground prose-th:font-semibold prose-th:text-left prose-th:py-2 prose-th:px-3",
      "prose-td:text-foreground prose-tr:border-b prose-tr:border-border prose-td:py-2 prose-td:px-3",

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
