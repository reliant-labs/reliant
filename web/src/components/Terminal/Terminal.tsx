import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";
import { logger } from "../../lib/logger";
import { supabase } from "../../lib/supabase";
import { cn } from "../../lib/utils";
import { useTerminalStore } from "../../store/terminalStore";
import { useSidebarStore } from "../../store/sidebarStore";
import { getConfiguredGatewayURL } from "../../api/grpc-client";

interface TerminalProps {
  sessionId: string;
  workingDir?: string;
  worktreeId?: string;
  onExit?: () => void;
  className?: string;
}

export function Terminal({ sessionId, workingDir, worktreeId, className }: TerminalProps) {
  const terminalRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<XTerm | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const [isConnected, setIsConnected] = useState(false);
  const updateSessionPID = useTerminalStore((state) => state.updateSessionPID);
  const activeSessionId = useTerminalStore((state) => state.activeSessionId);

  // Main terminal initialization effect - only runs once per sessionId
  useEffect(() => {
    if (!terminalRef.current) return;

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
      // Use the configured daemon gateway URL for terminal WebSocket connections.
      const gatewayURL = getConfiguredGatewayURL();
      if (!gatewayURL) {
        logger.error("[Terminal] No daemon gateway URL configured, terminal unavailable");
        term.writeln("\r\n\x1b[91mTerminal unavailable — no gateway URL configured.\x1b[0m");
        return () => {};
      }

      const protocol = gatewayURL.startsWith("https") ? "wss:" : "ws:";
      const host = gatewayURL.replace(/^https?:\/\//, "");
      logger.info("[Terminal] Using daemon gateway for terminal WS", { gatewayURL });

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
        setIsConnected(true);
        
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
              // Initial message with PID
              if (data.pid) {
                updateSessionPID(sessionId, data.pid);
                logger.info("[Terminal] Received PID", { sessionId, pid: data.pid });
              }
            } else if (data.type === "output") {
              term.write(data.data);
            } else if (data.type === "error") {
              term.write(`\r\n\x1b[91mError: ${data.data}\x1b[0m\r\n`);
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
        logger.error("[Terminal] WebSocket error", { error });
        term.write("\r\n\x1b[91mConnection error\x1b[0m\r\n");
      };

      ws.onclose = () => {
        logger.info("[Terminal] WebSocket closed");
        setIsConnected(false);
        term.write("\r\n\x1b[93mConnection closed\x1b[0m\r\n");
      };

      // Handle terminal input
      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) {
          // Send raw data directly - xterm.js already provides the correct bytes
          // Including escape sequences for special keys (arrows, backspace, etc.)
          ws.send(data);
        }
      });

      // Handle terminal resize
      const handleResize = () => {
        if (!fitAddonRef.current || !xtermRef.current) return;

        fitAddonRef.current.fit();

        if (ws.readyState === WebSocket.OPEN) {
          // Resize messages must be JSON
          ws.send(JSON.stringify({
            type: "resize",
            cols: xtermRef.current.cols,
            rows: xtermRef.current.rows,
          }));
        }
      };

      // Setup resize observer
      const resizeObserver = new ResizeObserver(handleResize);
      if (terminalRef.current) {
        resizeObserver.observe(terminalRef.current);
      }

      return () => {
        resizeObserver.disconnect();
        if (ws.readyState === WebSocket.OPEN) {
          ws.close();
        }
      };
    };

    let cleanup: (() => void) | undefined;
    connectWebSocket().then(fn => { cleanup = fn; });

    // Cleanup
    return () => {
      logger.info("[Terminal] Cleaning up terminal");

      if (cleanup) {
        cleanup();
      }

      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.close();
      }

      term.dispose();
    };
  }, [sessionId, workingDir, worktreeId, updateSessionPID]); // Only depend on sessionId, workingDir, worktreeId, and updateSessionPID

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

  return (
    <div className={cn("relative w-full h-full", className)}>
      <div
        ref={terminalRef}
        className="w-full h-full"
      />
      {!isConnected && (
        <div className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border text-foreground px-4 py-3 rounded-lg shadow-lg font-mono text-xs">
          Connecting to terminal...
        </div>
      )}
    </div>
  );
}