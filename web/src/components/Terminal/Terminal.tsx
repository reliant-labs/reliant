import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import { Loader2 } from "lucide-react";
import "@xterm/xterm/css/xterm.css";
import { logger } from "../../lib/logger";
import { supabase } from "../../lib/supabase";
import { cn } from "../../lib/utils";
import { isDaemonConnectingError } from "../../lib/daemon-errors";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useTerminalStore } from "../../store/terminalStore";
import { useSidebarStore } from "../../store/sidebarStore";
import { getGRPCBaseURLPublic } from "../../api/grpc-client";

interface TerminalProps {
  sessionId: string;
  workingDir?: string;
  worktreeId?: string;
  onExit?: () => void;
  className?: string;
}

const WS_RECONNECT_BASE_DELAY = 1000;
const WS_RECONNECT_MAX_DELAY = 20000;
const WS_MAX_RECONNECT_ATTEMPTS = 10;
// Slow fallback retry cadence while the daemon is known to be offline. The
// real reconnect trigger in that state is the daemon-status subscription
// flipping to online; this timer only guards against stale client-side status.
const DAEMON_OFFLINE_RETRY_DELAY = 30000;

/**
 * Terminal connection lifecycle:
 * - connecting:         initial session attempt (or a retry after the daemon came back)
 * - connected:          session established (server sent "init")
 * - reconnecting:       session dropped while a daemon is online; auto-retrying with backoff
 * - waiting_for_daemon: no daemon connected; retries are gated on daemon status
 * - disconnected:       terminal ended or retries exhausted; manual reconnect offered
 */
type TerminalConnectionState =
  | "connecting"
  | "connected"
  | "reconnecting"
  | "waiting_for_daemon"
  | "disconnected";

export function Terminal({ sessionId, workingDir, worktreeId, className }: TerminalProps) {
  const terminalRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<XTerm | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reconnectAttemptsRef = useRef(0);
  const [connectionState, setConnectionState] = useState<TerminalConnectionState>("connecting");
  const connectionStateRef = useRef<TerminalConnectionState>("connecting");
  // Set when the server reports "no daemon connected" for the current attempt.
  const daemonUnavailableRef = useRef(false);
  const connectWebSocketRef = useRef<(() => Promise<(() => void) | undefined>) | null>(null);
  const updateSessionPID = useTerminalStore((state) => state.updateSessionPID);
  const activeSessionId = useTerminalStore((state) => state.activeSessionId);

  const updateConnectionState = useCallback((next: TerminalConnectionState) => {
    connectionStateRef.current = next;
    setConnectionState(next);
  }, []);

  // Daemon connectivity from the shared (deduped, 5s-poll) daemon status
  // query. Used to classify a dropped session as "daemon offline" vs "session
  // dropped", and to reconnect promptly when the daemon comes online instead
  // of hammering the server while it is down. While the status is still
  // loading we assume online — the server's own error is the stronger signal.
  const { activeDaemon, loading: daemonStatusLoading } = useDaemonStatus();
  const daemonOnline = !!activeDaemon || daemonStatusLoading;
  const daemonOnlineRef = useRef(daemonOnline);

  useEffect(() => {
    const wasOnline = daemonOnlineRef.current;
    daemonOnlineRef.current = daemonOnline;
    if (!wasOnline && daemonOnline && connectionStateRef.current === "waiting_for_daemon") {
      logger.info("[Terminal] Daemon came online, reconnecting", { sessionId });
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
      }
      reconnectAttemptsRef.current = 0;
      updateConnectionState("connecting");
      void connectWebSocketRef.current?.();
    }
  }, [daemonOnline, sessionId, updateConnectionState]);

  // Main terminal initialization effect - only runs once per sessionId
  useEffect(() => {
    if (!terminalRef.current) return;

    // Effect-scoped disposal flag: each run gets its own, so a stale socket's
    // async close event can never be mistaken for the current run's socket.
    let disposed = false;

    // Fresh run: reset connection bookkeeping (refs survive effect re-runs).
    reconnectAttemptsRef.current = 0;
    daemonUnavailableRef.current = false;
    updateConnectionState("connecting");

    // Get theme colors from CSS variables
    const getColor = (variable: string) => {
      const hsl = getComputedStyle(document.documentElement).getPropertyValue(variable);
      if (!hsl) return '#000000';
      return `hsl(${hsl})`;
    };

    // Check if dark theme by checking background lightness
    const bgHsl = getComputedStyle(document.documentElement).getPropertyValue('--background');
    const isDark = bgHsl ? parseInt(bgHsl.split(' ')[2]) < 50 : false;

    // Create terminal instance with theme-aware colors
    const term = new XTerm({
      // Basic configuration
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      scrollback: 10000,
      rows: 24,
      cols: 80,
      // Input/output configuration - critical for proper terminal behavior
      disableStdin: false,
      // NOTE: convertEol should NOT be used with a real PTY - the PTY handles line ending conversion
      // Using convertEol with a PTY causes input corruption and backspace issues
      // Cursor configuration
      cursorBlink: true,
      cursorStyle: 'block',
      cursorWidth: 1,
      // Interaction
      macOptionIsMeta: true,
      rightClickSelectsWord: true,
      altClickMovesCursor: false,
      // Scrolling
      fastScrollSensitivity: 5,
      scrollSensitivity: 1,
      // Display
      wordSeparator: ' ()[]{}\'"',
      screenReaderMode: false,
      drawBoldTextInBrightColors: true,
      allowTransparency: false,
      // Window configuration
      windowOptions: {
        setWinSizePixels: true,
      },
      // Theme colors
      theme: {
        background: getColor('--background'),
        foreground: getColor('--foreground'),
        cursor: getColor('--primary'),
        cursorAccent: getColor('--background'),
        selectionBackground: getColor('--accent'),
        selectionForeground: getColor('--accent-foreground'),
        // ANSI colors - keeping standard terminal colors but adjusting for theme
        // In light mode, use dark colors for visibility on light backgrounds
        black: isDark ? "#000000" : "#1a1a1a",
        red: isDark ? "#f14c4c" : "#cd3131",
        green: isDark ? "#23d18b" : "#0a6640",
        yellow: isDark ? "#f5f543" : "#8a7400",
        blue: isDark ? getColor('--primary') : "#0451a5",
        magenta: isDark ? getColor('--secondary') : "#8b008b",
        cyan: isDark ? "#29b8db" : "#1a1a1a",
        white: isDark ? "#e5e5e5" : "#1a1a1a",
        brightBlack: isDark ? "#666666" : "#555555",
        brightRed: isDark ? "#f14c4c" : "#cd3131",
        brightGreen: isDark ? "#23d18b" : "#0a6640",
        brightYellow: isDark ? "#f5f543" : "#8a7400",
        brightBlue: isDark ? getColor('--primary') : "#0451a5",
        brightMagenta: isDark ? getColor('--secondary') : "#8b008b",
        brightCyan: isDark ? "#29b8db" : "#1a1a1a",
        brightWhite: isDark ? getColor('--foreground') : "#1a1a1a",
      },
      // Allow proposed API for advanced features
      allowProposedApi: true,
    });

    // Create and load addons
    const fitAddon = new FitAddon();
    const webLinksAddon = new WebLinksAddon();

    term.loadAddon(fitAddon);
    term.loadAddon(webLinksAddon);

    // Open terminal
    term.open(terminalRef.current);

    // Load WebGL addon after opening for better rendering performance
    // Falls back to DOM renderer if WebGL is not available
    try {
      const webglAddon = new WebglAddon();
      webglAddon.onContextLoss(() => {
        webglAddon.dispose();
      });
      term.loadAddon(webglAddon);
    } catch (e) {
      logger.warn('[Terminal] WebGL addon failed to load, using DOM renderer', { error: e });
    }

    // Fit the terminal - retry until container has proper dimensions
    // The terminal container might be hidden initially (display:none from parent)
    let fitAttempts = 0;
    const maxFitAttempts = 20; // Try for up to 2 seconds
    
    const doInitialFit = () => {
      const container = terminalRef.current;
      if (!container) return;
      
      const rect = container.getBoundingClientRect();
      
      // Check if container is visible and has reasonable dimensions
      if (rect.width < 100 || rect.height < 100) {
        fitAttempts++;
        if (fitAttempts < maxFitAttempts) {
          // Container still hidden or too small, retry
          setTimeout(doInitialFit, 100);
        } else {
          logger.warn('[Terminal] Initial fit timeout - container never became visible', {
            width: rect.width,
            height: rect.height,
            attempts: fitAttempts
          });
        }
        return;
      }
      
      try {
        fitAddon.fit();
        logger.info('[Terminal] Initial fit successful', {
          cols: term.cols,
          rows: term.rows,
          width: rect.width,
          height: rect.height,
          attempts: fitAttempts + 1
        });
      } catch (error) {
        logger.warn('[Terminal] Initial fit failed', { error });
      }
    };
    
    // Start trying to fit after a short delay
    setTimeout(doInitialFit, 100);

    // Attach custom key event handler to allow app shortcuts to work
    term.attachCustomKeyEventHandler((event: KeyboardEvent) => {
      // Check for toggle terminal shortcut (Cmd+J or Ctrl+J)
      if (event.key === 'j' && (event.metaKey || event.ctrlKey) && !event.shiftKey) {
        // Let the event bubble up to be handled by app shortcuts
        return false;
      }

      // Check for new terminal shortcut (Cmd+Shift+J or Ctrl+Shift+J)
      if (event.key === 'j' && (event.metaKey || event.ctrlKey) && event.shiftKey) {
        // Let the event bubble up to be handled by app shortcuts
        return false;
      }

      // Allow all other keys to be handled by xterm
      return true;
    });

    xtermRef.current = term;
    fitAddonRef.current = fitAddon;

    // Connect to WebSocket
    const connectWebSocket = async () => {
      if (disposed) return;

      // Per-attempt evidence: cleared here, set by this attempt's messages.
      daemonUnavailableRef.current = false;

      // Use the main gRPC base URL for terminal WebSocket connections.
      const baseURL = getGRPCBaseURLPublic();
      if (!baseURL) {
        logger.error("[Terminal] No gRPC base URL configured, terminal unavailable");
        term.writeln("\r\n\x1b[91mTerminal unavailable — no base URL configured.\x1b[0m");
        updateConnectionState("disconnected");
        return () => {};
      }

      const protocol = baseURL.startsWith("https") ? "wss:" : "ws:";
      const host = baseURL.replace(/^https?:\/\//, "");
      logger.info("[Terminal] Using gRPC base URL for terminal WS", { baseURL });

      // Get auth token
      const { data: { session } } = await supabase.auth.getSession();
      const token = session?.access_token || "";

      const params = new URLSearchParams();
      if (workingDir) params.append("workingDir", workingDir);
      if (worktreeId) params.append("worktreeId", worktreeId);
      if (token) params.append("token", token);

      const wsUrl = `${protocol}//${host}/api/v2/terminal/ws?${params.toString()}`;

      logger.info("[Terminal] Connecting to WebSocket", { url: wsUrl.replace(token, "***") });

      const ws = new WebSocket(wsUrl);
      wsRef.current = ws;

      ws.onopen = () => {
        logger.info("[Terminal] WebSocket connected");
        // NOTE: an open socket does not mean the session exists — the server
        // creates the daemon session after the upgrade and confirms it with
        // the "init" message. Marking success (and resetting the backoff
        // counter) here made every failed session-create look like attempt 1,
        // producing a tight retry loop with no backoff while the daemon was
        // offline. Success is handled in the "init" branch below.

        // Send initial resize to sync terminal dimensions
        setTimeout(() => {
          if (fitAddonRef.current && xtermRef.current && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({
              type: "resize",
              cols: xtermRef.current.cols,
              rows: xtermRef.current.rows,
            }));
            logger.info("[Terminal] Sent initial resize", {
              cols: xtermRef.current.cols,
              rows: xtermRef.current.rows
            });
          }
        }, 100);
      };

      ws.onmessage = (event) => {
        try {
          // Try to parse as JSON - only handle if it's an object with a type property
          const data = JSON.parse(event.data);

          // Check if it's a structured message (object with type property)
          // This prevents raw characters like digits from being parsed as JSON numbers
          if (data && typeof data === 'object' && typeof data.type === 'string') {
            if (data.type === "init") {
              // Session created on the daemon — the connection is healthy.
              // This (not ws.onopen) is the success signal: reset the backoff.
              reconnectAttemptsRef.current = 0;
              daemonUnavailableRef.current = false;
              updateConnectionState("connected");
              if (data.pid) {
                updateSessionPID(sessionId, data.pid);
                logger.info("[Terminal] Received PID", { sessionId, pid: data.pid });
              }
            } else if (data.type === "output") {
              term.write(data.data);
            } else if (data.type === "error") {
              if (isDaemonConnectingError(data.data)) {
                // "no daemon connected" — expected while the daemon is
                // offline. The waiting overlay owns the messaging; writing a
                // red error per retry was spamming the buffer.
                daemonUnavailableRef.current = true;
                logger.info("[Terminal] Session unavailable: no daemon connected", { sessionId });
              } else {
                term.write(`\r\n\x1b[91mError: ${data.data}\x1b[0m\r\n`);
              }
            } else if (data.type === "exit") {
              term.write(`\r\n\x1b[93m${data.data}\x1b[0m\r\n`);
            }
          } else {
            // Parsed JSON but not a structured message - treat as raw output
            term.write(event.data);
          }
        } catch {
          // Not valid JSON - treat as raw text output
          term.write(event.data);
        }
      };

      ws.onerror = (error) => {
        // onclose always follows and owns the user-visible state; writing
        // "Connection error" here repeated it into the buffer on every retry.
        logger.error("[Terminal] WebSocket error", { error });
      };

      ws.onclose = (event) => {
        logger.info("[Terminal] WebSocket closed", { code: event.code, reason: event.reason });

        // Intentionally closed (component unmount / effect re-run): the
        // terminal instance is disposed, do nothing.
        if (disposed) return;

        // Normal exit (e.g. shell exited) — code 1000 means clean close.
        if (event.code === 1000) {
          term.write("\r\n\x1b[93mSession ended\x1b[0m\r\n");
          updateConnectionState("disconnected");
          return;
        }

        // Daemon offline — either the server said "no daemon connected" for
        // this attempt, or the shared daemon status shows nothing connected.
        // Don't burn backoff attempts: show a calm persistent waiting state
        // and let the daemon-status subscription trigger the reconnect, with
        // a slow fallback retry in case that status is stale.
        if (daemonUnavailableRef.current || !daemonOnlineRef.current) {
          if (connectionStateRef.current !== "waiting_for_daemon") {
            term.write("\r\n\x1b[93mWaiting for daemon to come online...\x1b[0m\r\n");
            updateConnectionState("waiting_for_daemon");
          }
          reconnectTimerRef.current = setTimeout(() => {
            logger.info("[Terminal] Daemon-offline fallback retry", { sessionId });
            connectWebSocket();
          }, DAEMON_OFFLINE_RETRY_DELAY);
          return;
        }

        if (reconnectAttemptsRef.current >= WS_MAX_RECONNECT_ATTEMPTS) {
          term.write("\r\n\x1b[91mConnection lost. Max reconnect attempts reached.\x1b[0m\r\n");
          updateConnectionState("disconnected");
          return;
        }

        const attempt = reconnectAttemptsRef.current + 1;
        reconnectAttemptsRef.current = attempt;
        const baseDelay = Math.min(
          WS_RECONNECT_BASE_DELAY * Math.pow(2, attempt - 1),
          WS_RECONNECT_MAX_DELAY
        );
        // Small jitter so many mounted terminals don't retry in lockstep.
        const delay = Math.round(baseDelay * (0.85 + Math.random() * 0.3));

        // Announce the reconnect episode once — the overlay reflects ongoing
        // state; a per-attempt write spammed the buffer.
        if (connectionStateRef.current !== "reconnecting") {
          term.write("\r\n\x1b[93mConnection lost. Reconnecting...\x1b[0m\r\n");
          updateConnectionState("reconnecting");
        }

        reconnectTimerRef.current = setTimeout(() => {
          logger.info("[Terminal] Reconnecting", { attempt, delay });
          connectWebSocket();
        }, delay);
      };

      return () => {
        if (ws.readyState === WebSocket.OPEN) {
          ws.close();
        }
      };
    };

    // Handle terminal input — use wsRef so reconnects work.
    term.onData((data) => {
      const currentWs = wsRef.current;
      if (currentWs?.readyState === WebSocket.OPEN) {
        currentWs.send(data);
      }
    });

    // Handle terminal resize — use wsRef so reconnects work.
    const handleResize = () => {
      if (!fitAddonRef.current || !xtermRef.current) return;

      fitAddonRef.current.fit();

      const currentWs = wsRef.current;
      if (currentWs?.readyState === WebSocket.OPEN) {
        currentWs.send(JSON.stringify({
          type: "resize",
          cols: xtermRef.current.cols,
          rows: xtermRef.current.rows,
        }));
      }
    };

    // Setup resize observer (once per mount)
    const resizeObserver = new ResizeObserver(handleResize);
    if (terminalRef.current) {
      resizeObserver.observe(terminalRef.current);
    }

    connectWebSocketRef.current = connectWebSocket;

    let wsCleanup: (() => void) | undefined;
    connectWebSocket().then(fn => { wsCleanup = fn; });

    // Cleanup
    return () => {
      logger.info("[Terminal] Cleaning up terminal");
      disposed = true;

      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
      }

      resizeObserver.disconnect();

      if (wsCleanup) {
        wsCleanup();
      }

      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.close();
      }

      term.dispose();
    };
  }, [sessionId, workingDir, worktreeId, updateSessionPID, updateConnectionState]); // updateConnectionState is a stable setter

  // Listen for theme changes and update terminal colors
  useEffect(() => {
    if (!xtermRef.current) return;

    const updateTheme = () => {
      if (!xtermRef.current) return;

      const getColor = (variable: string) => {
        const hsl = getComputedStyle(document.documentElement).getPropertyValue(variable);
        if (!hsl) return '#000000';
        return `hsl(${hsl})`;
      };

      const bgHsl = getComputedStyle(document.documentElement).getPropertyValue('--background');
      const isDark = bgHsl ? parseInt(bgHsl.split(' ')[2]) < 50 : false;

      xtermRef.current.options.theme = {
        background: getColor('--background'),
        foreground: getColor('--foreground'),
        cursor: getColor('--primary'),
        cursorAccent: getColor('--background'),
        selectionBackground: getColor('--accent'),
        selectionForeground: getColor('--accent-foreground'),
        // In light mode, use dark colors for visibility on light backgrounds
        black: isDark ? "#000000" : "#1a1a1a",
        red: isDark ? "#f14c4c" : "#cd3131",
        green: isDark ? "#23d18b" : "#0a6640",
        yellow: isDark ? "#f5f543" : "#8a7400",
        blue: isDark ? getColor('--primary') : "#0451a5",
        magenta: isDark ? getColor('--secondary') : "#8b008b",
        cyan: isDark ? "#29b8db" : "#1a1a1a",
        white: isDark ? "#e5e5e5" : "#1a1a1a",
        brightBlack: isDark ? "#666666" : "#555555",
        brightRed: isDark ? "#f14c4c" : "#cd3131",
        brightGreen: isDark ? "#23d18b" : "#0a6640",
        brightYellow: isDark ? "#f5f543" : "#8a7400",
        brightBlue: isDark ? getColor('--primary') : "#0451a5",
        brightMagenta: isDark ? getColor('--secondary') : "#8b008b",
        brightCyan: isDark ? "#29b8db" : "#1a1a1a",
        brightWhite: isDark ? getColor('--foreground') : "#1a1a1a",
      };
    };

    // Update theme on mount
    updateTheme();

    // Listen for theme changes via MutationObserver on documentElement's attributes
    const observer = new MutationObserver((mutations) => {
      mutations.forEach((mutation) => {
        if (mutation.type === 'attributes' && 
            (mutation.attributeName === 'class' || mutation.attributeName === 'data-color-scheme')) {
          updateTheme();
        }
      });
    });

    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'data-color-scheme']
    });

    return () => {
      observer.disconnect();
    };
  }, []);

  // Listen for clear terminal event from Electron menu
  useEffect(() => {
    const handleClear = () => {
      // Only clear if this is the active terminal session
      if (sessionId === activeSessionId && xtermRef.current) {
        xtermRef.current.clear();
        logger.debug('[Terminal] Cleared terminal via keyboard shortcut', { sessionId });
      }
    };

    window.addEventListener('clear-active-terminal', handleClear);
    return () => window.removeEventListener('clear-active-terminal', handleClear);
  }, [sessionId, activeSessionId]);

  // Auto-focus terminal when it becomes active
  useEffect(() => {
    if (sessionId === activeSessionId && xtermRef.current) {
      // Focus the terminal when it becomes the active session
      xtermRef.current.focus();
      logger.debug('[Terminal] Auto-focused active terminal', { sessionId });
    }
  }, [sessionId, activeSessionId]);

  // Listen for focus-active-terminal event
  useEffect(() => {
    const handleFocusTerminal = (event: CustomEvent) => {
      if (event.detail?.sessionId === sessionId && xtermRef.current) {
        xtermRef.current.focus();
        logger.debug('[Terminal] Focused terminal via event', { sessionId });
      }
    };

    window.addEventListener('focus-active-terminal', handleFocusTerminal as EventListener);
    return () => window.removeEventListener('focus-active-terminal', handleFocusTerminal as EventListener);
  }, [sessionId]);

  // Listen for general focus-terminal event (when Cmd+J is pressed while terminal is open but not focused)
  useEffect(() => {
    const handleFocusTerminalGeneral = () => {
      if (sessionId === activeSessionId && xtermRef.current) {
        xtermRef.current.focus();
        logger.debug('[Terminal] Focused active terminal via toggle shortcut', { sessionId });
      }
    };

    window.addEventListener('focus-terminal', handleFocusTerminalGeneral);
    return () => window.removeEventListener('focus-terminal', handleFocusTerminalGeneral);
  }, [sessionId, activeSessionId]);

  // Listen for terminal container changes and refit
  useEffect(() => {
    const handleContainerChange = () => {
      if (fitAddonRef.current && xtermRef.current) {
        // Try fitting immediately and with delays
        const tryFit = () => {
          if (fitAddonRef.current) {
            try {
              fitAddonRef.current.fit();
              logger.debug('[Terminal] Refitted terminal after container change', { sessionId });
            } catch (error) {
              logger.warn('[Terminal] Failed to refit terminal', { sessionId, error });
            }
          }
        };

        tryFit();
        setTimeout(tryFit, 50);
        setTimeout(tryFit, 150);
      }
    };

    const handleResize = () => {
      if (fitAddonRef.current && xtermRef.current) {
        try {
          fitAddonRef.current.fit();
        } catch (error) {
          logger.error('[Terminal] Failed to refit terminal', { error });
          // Ignore errors during resize
        }
      }
    };

    window.addEventListener('terminal-container-changed', handleContainerChange);
    window.addEventListener('resize', handleResize);

    return () => {
      window.removeEventListener('terminal-container-changed', handleContainerChange);
      window.removeEventListener('resize', handleResize);
    };
  }, [sessionId]);

  // Listen for sidebar dimension changes and refit terminal
  const sidebarWidth = useSidebarStore((state) => state.width);
  const diffHeightPercent = useSidebarStore((state) => state.diffHeightPercent);

  useEffect(() => {
    if (fitAddonRef.current && xtermRef.current) {
      const tryFit = () => {
        if (fitAddonRef.current) {
          try {
            fitAddonRef.current.fit();
            logger.debug('[Terminal] Refitted after sidebar resize', { sessionId, sidebarWidth, diffHeightPercent });
          } catch (error) {
            logger.warn('[Terminal] Failed to refit after sidebar resize', { sessionId, error });
          }
        }
      };

      // Delay to ensure DOM has updated
      setTimeout(tryFit, 50);
      setTimeout(tryFit, 150);
    }
  }, [sessionId, sidebarWidth, diffHeightPercent]);

  const handleReconnectClick = () => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current);
      reconnectTimerRef.current = null;
    }
    reconnectAttemptsRef.current = 0;
    daemonUnavailableRef.current = false;
    updateConnectionState("connecting");
    void connectWebSocketRef.current?.();
  };

  return (
    <div className={cn("relative w-full h-full", className)}>
      <div
        ref={terminalRef}
        className="w-full h-full"
      />
      {connectionState === "connecting" && (
        <div
          className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border text-foreground px-4 py-3 rounded-lg shadow-lg flex items-center gap-2 font-mono text-xs"
          role="status"
          aria-live="polite"
        >
          <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" aria-hidden="true" />
          Connecting to terminal...
        </div>
      )}
      {connectionState === "reconnecting" && (
        <div
          className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border text-foreground px-4 py-3 rounded-lg shadow-lg flex items-center gap-2 font-mono text-xs"
          role="status"
          aria-live="polite"
        >
          <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" aria-hidden="true" />
          Reconnecting...
        </div>
      )}
      {connectionState === "waiting_for_daemon" && (
        <div
          className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border text-foreground px-4 py-3 rounded-lg shadow-lg flex flex-col items-center gap-1.5"
          role="status"
          aria-live="polite"
        >
          <div className="flex items-center gap-2 font-mono text-xs">
            <span
              className="h-2 w-2 rounded-full bg-yellow-500 animate-pulse shadow-[0_0_0_3px_rgba(234,179,8,0.15)]"
              aria-hidden="true"
            />
            Waiting for daemon to come online...
          </div>
          <span className="text-xs text-muted-foreground">The terminal will connect automatically.</span>
        </div>
      )}
      {connectionState === "disconnected" && (
        <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border text-foreground px-4 py-3 rounded-lg shadow-lg flex flex-col items-center gap-2">
          <span className="font-mono text-xs text-muted-foreground">Terminal disconnected</span>
          <button
            onClick={handleReconnectClick}
            className="px-3 py-1.5 text-xs font-medium rounded-md bg-primary text-primary-foreground hover:bg-primary/90 transition-colors"
          >
            Reconnect
          </button>
        </div>
      )}
    </div>
  );
}