/**
 * Videos a tool produced, rendered as content in the conversation.
 *
 * Sibling of MessageGeneratedImages, and deliberately not merged into it: an
 * image is fetched whole through gRPC and shown from a blob URL, but a clip can
 * be tens of MB. Here the <video> element streams straight from the
 * Range-capable `/api/attachments/{id}/content` endpoint, so it can seek and
 * start playing without the bytes ever passing through JS.
 *
 * A <video> element cannot set an Authorization header, so the bearer token
 * rides as a `token` query parameter — the same arrangement the terminal
 * WebSocket uses.
 */

import { useEffect, useState } from "react";
import { Download, VideoOff } from "lucide-react";
import type { Attachment } from "../../types/chat";
import { cn } from "../../lib/utils";
import { getAuthTokenProvider } from "../../api/authProvider";
import { getGRPCBaseURLPublic } from "../../api/grpc-client";

export function isVideoMimeType(mimeType: string | undefined): boolean {
  return Boolean(mimeType?.startsWith("video/"));
}

/** Absolute, authenticated URL of an attachment's streamable content. */
export async function buildAttachmentContentUrl(
  attachmentId: string,
): Promise<string> {
  const base = (getGRPCBaseURLPublic() ?? "").replace(/\/$/, "");
  const path = `${base}/api/attachments/${encodeURIComponent(attachmentId)}/content`;
  const token = await getAuthTokenProvider().getToken();
  return token ? `${path}?token=${encodeURIComponent(token)}` : path;
}

function useAttachmentContentUrl(attachmentId: string): string | null {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    void buildAttachmentContentUrl(attachmentId).then((built) => {
      if (!cancelled) setUrl(built);
    });
    return () => {
      cancelled = true;
    };
  }, [attachmentId]);
  return url;
}

interface MessageGeneratedVideosProps {
  attachments: Attachment[];
  className?: string;
}

function GeneratedVideo({ attachment }: { attachment: Attachment }) {
  const url = useAttachmentContentUrl(attachment.id);
  const [failed, setFailed] = useState(false);

  if (failed) {
    return (
      <div
        className="flex aspect-video flex-col items-center justify-center gap-1.5 rounded-lg border border-border bg-card text-muted-foreground"
        role="img"
        aria-label={`${attachment.filename} failed to load`}
      >
        <VideoOff className="h-5 w-5" aria-hidden="true" />
        <span className="px-2 text-center text-2xs">
          Could not load video. It may have expired.
        </span>
      </div>
    );
  }

  if (!url) {
    return (
      <div
        className="aspect-video animate-pulse rounded-lg border border-border bg-card"
        aria-label={`Loading ${attachment.filename}`}
        aria-busy="true"
      />
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card">
      <video
        src={url}
        controls
        preload="metadata"
        playsInline
        className="h-auto w-full bg-black"
        data-testid="generated-video"
        onError={() => setFailed(true)}
      />
      <div className="flex items-center justify-between gap-2 border-t border-border/60 px-2 py-1 text-2xs text-muted-foreground">
        <span className="truncate">{attachment.filename}</span>
        <a
          href={url}
          download={attachment.filename}
          className="inline-flex items-center gap-1 hover:text-foreground"
          aria-label={`Download ${attachment.filename}`}
        >
          <Download className="h-3 w-3" aria-hidden="true" />
          Download
        </a>
      </div>
    </div>
  );
}

export function MessageGeneratedVideos({
  attachments,
  className,
}: MessageGeneratedVideosProps) {
  const videos = attachments.filter((a) => isVideoMimeType(a.mimeType));
  if (videos.length === 0) return null;

  return (
    <div
      className={cn("my-2 flex max-w-xl flex-col gap-2", className)}
      data-testid="message-generated-videos"
    >
      {videos.map((attachment) => (
        <GeneratedVideo key={attachment.id} attachment={attachment} />
      ))}
    </div>
  );
}
