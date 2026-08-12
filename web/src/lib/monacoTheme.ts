import type { Monaco } from "@monaco-editor/react";
import { initializeLanguageFeatures } from './monacoLanguageFeatures';
import { CEL_DARK_TOKEN_RULES, CEL_LIGHT_TOKEN_RULES } from './monaco-cel-language';

/**
 * The monospace stack every Monaco instance should use.
 *
 * Monaco measures character width from its own `fontFamily` option, not from
 * whatever CSS ends up painting the glyphs. Leaving the option unset while
 * index.css restyles `.monaco-editor` makes the cursor and selection drift
 * away from the text, so pass this to every editor's options and let the CSS
 * rules agree with it. Leads with the bundled JetBrains Mono
 * (web/public/fonts) so the editor looks the same on every platform.
 */
export const MONACO_FONT_FAMILY =
  "'JetBrains Mono', ui-monospace, 'SF Mono', SFMono-Regular, Menlo, Monaco, Consolas, monospace";

// Helper to get CSS variable as hex
const getCSSVar = (varName: string): string => {
  const root = document.documentElement;
  const computedStyle = getComputedStyle(root);
  const value = computedStyle.getPropertyValue(varName).trim();
  
  // If it's an HSL value, convert to hex
  if (value.includes(' ')) {
    const [h, s, l] = value.split(' ').map(v => parseFloat(v.replace('%', '')));
    return hslToHex(h, s, l);
  }
  return value || '#000000';
};

// HSL to Hex converter
const hslToHex = (h: number, s: number, l: number): string => {
  l /= 100;
  const a = s * Math.min(l, 1 - l) / 100;
  const f = (n: number) => {
    const k = (n + h / 30) % 12;
    const color = l - a * Math.max(Math.min(k - 3, 9 - k, 1), -1);
    return Math.round(255 * color).toString(16).padStart(2, '0');
  };
  return `#${f(0)}${f(8)}${f(4)}`;
};

// Helper to get CSS variable as hex with alpha (for Monaco)
const getCSSVarWithAlpha = (varName: string, opacity: number): string => {
  const root = document.documentElement;
  const computedStyle = getComputedStyle(root);
  const value = computedStyle.getPropertyValue(varName).trim();
  
  // If it's an HSL value, convert to hex with alpha
  if (value.includes(' ')) {
    const [h, s, l] = value.split(' ').map(v => parseFloat(v.replace('%', '')));
    return hslToHexWithAlpha(h, s, l, opacity);
  }
  // If it's already a hex color, add alpha
  if (value.startsWith('#')) {
    return hexToRgba(value, opacity);
  }
  // Fallback
  const alpha = Math.round(opacity * 255).toString(16).padStart(2, '0');
  return `#000000${alpha}`;
};

// Hex to RGBA converter (returns hex with alpha for Monaco)
const hexToRgba = (hex: string, a: number): string => {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  const alpha = Math.round(a * 255).toString(16).padStart(2, '0');
  return `#${r.toString(16).padStart(2, '0')}${g.toString(16).padStart(2, '0')}${b.toString(16).padStart(2, '0')}${alpha}`;
};

// HSL to Hex with alpha converter (for Monaco)
const hslToHexWithAlpha = (h: number, s: number, l: number, a: number): string => {
  l /= 100;
  const a_val = s * Math.min(l, 1 - l) / 100;
  const f = (n: number) => {
    const k = (n + h / 30) % 12;
    const color = l - a_val * Math.max(Math.min(k - 3, 9 - k, 1), -1);
    return Math.round(255 * color).toString(16).padStart(2, '0');
  };
  const alpha = Math.round(a * 255).toString(16).padStart(2, '0');
  return `#${f(0)}${f(8)}${f(4)}${alpha}`;
};

/**
 * Adds type definitions for common libraries to Monaco
 */
export async function addTypeDefinitions(monaco: Monaco): Promise<void> {
  try {
    
    // Add basic React types (minimal, to avoid errors)
    const basicReactTypes = `
      // Make everything very permissive to avoid false errors
      declare module 'react' {
        export function useState<T>(initialValue: T): [T, (value: T) => void];
        export function useEffect(effect: () => void | (() => void), deps?: any[]): void;
        export function useCallback<T extends (...args: any[]) => any>(callback: T, deps: any[]): T;
        export function useMemo<T>(factory: () => T, deps: any[]): T;
        export function useRef<T>(initialValue: T): { current: T };
        export function useContext<T>(context: any): T;
        export function useReducer<R extends Reducer<any, any>>(
          reducer: R,
          initialState: ReducerState<R>
        ): [ReducerState<R>, Dispatch<ReducerAction<R>>];
        
        export type FC<P = {}> = (props: P) => JSX.Element | null;
        export type ReactNode = any;
        export type ReactElement = any;
        export type CSSProperties = any;
        export type MouseEvent<T = Element> = any;
        export type ChangeEvent<T = Element> = any;
        export type FormEvent<T = Element> = any;
        export type KeyboardEvent<T = Element> = any;
        export type FocusEvent<T = Element> = any;
        
        export interface Reducer<S, A> {
          (state: S, action: A): S;
        }
        export type ReducerState<R extends Reducer<any, any>> = R extends Reducer<infer S, any> ? S : never;
        export type ReducerAction<R extends Reducer<any, any>> = R extends Reducer<any, infer A> ? A : never;
        export type Dispatch<A> = (value: A) => void;
        
        export const Fragment: any;
        export const StrictMode: any;
        export const Suspense: any;
        export const lazy: any;
        export const memo: any;
        export const forwardRef: any;
        export const createContext: any;
      }
      
      declare module 'react-dom' {
        export function render(element: any, container: any): void;
        export function createRoot(container: any): any;
      }
      
      declare module 'react-dom/client' {
        export function createRoot(container: any): any;
      }
      
      declare namespace JSX {
        interface IntrinsicElements {
          [elemName: string]: any;
        }
        interface Element {}
        interface ElementAttributesProperty {
          props: {};
        }
        interface ElementChildrenAttribute {
          children: {};
        }
      }
      
      // Add common libraries to avoid import errors
      declare module '*.css' {
        const content: any;
        export default content;
      }
      
      declare module '*.scss' {
        const content: any;
        export default content;
      }
      
      declare module '*.module.css' {
        const classes: { [key: string]: string };
        export default classes;
      }
      
      declare module '*.module.scss' {
        const classes: { [key: string]: string };
        export default classes;
      }
      
      declare module '*.svg' {
        const content: any;
        export default content;
      }
      
      declare module '*.png' {
        const content: any;
        export default content;
      }
      
      declare module '*.jpg' {
        const content: any;
        export default content;
      }
      
      // Common utility libraries
      declare module 'classnames' {
        export default function classNames(...args: any[]): string;
      }
      
      declare module 'clsx' {
        export default function clsx(...args: any[]): string;
      }
      
      // Zustand
      declare module 'zustand' {
        export function create<T>(fn: any): any;
      }
      
      declare module 'zustand/middleware' {
        export function persist(fn: any, options: any): any;
      }
      
      // React Router
      declare module 'react-router-dom' {
        export const BrowserRouter: any;
        export const Route: any;
        export const Routes: any;
        export const Link: any;
        export const useNavigate: any;
        export const useParams: any;
        export const useLocation: any;
      }
      
      // Lucide React
      declare module 'lucide-react' {
        export const Icon: any;
        [key: string]: any;
      }
    `;
    
    monaco.languages.typescript.typescriptDefaults.addExtraLib(
      basicReactTypes,
      'file:///node_modules/@types/react/index.d.ts'
    );
  } catch (error) {
    console.warn('[Monaco] Failed to load type definitions:', error);
  }
}

/**
 * Configures Monaco diagnostics and language features
 * Delegates to the centralized language features service
 */
export function configureMonacoDiagnostics(monaco: Monaco): void {
  // Initialize the language features service (handles TypeScript config, validation, etc.)
  initializeLanguageFeatures(monaco, {
    enableDefinition: true,
    enableHover: true,
    enableValidation: false, // Keep validation off to avoid noise
  });

  // Load type definitions asynchronously
  addTypeDefinitions(monaco).catch(console.error);
}

/**
 * Configures the Reliant theme for Monaco Editor
 * This theme adapts to the app's light/dark mode using CSS variables
 */
export function configureMonacoTheme(monaco: Monaco): void {
  const isDarkMode = document.documentElement.classList.contains("dark");

  // Configure diagnostics globally
  configureMonacoDiagnostics(monaco);

  // Define both light and dark themes
  monaco.editor.defineTheme("reliant-dark", {
    base: "vs-dark",
    inherit: true,
    rules: [...CEL_DARK_TOKEN_RULES],
    colors: {
      "editor.lineHighlightBackground": "#2a2d2e",
      "editorLineNumber.foreground": "#858585",
      "editorLineNumber.activeForeground": "#c6c6c6",
      "editor.selectionBackground": "#264f78",
      "editor.inactiveSelectionBackground": "#1a3a52",
      "editorWidget.background": getCSSVar('--popover'),
      "editorWidget.border": getCSSVar('--border'),
      "editorSuggestWidget.background": getCSSVar('--popover'),
      "editorSuggestWidget.border": getCSSVar('--border'),
      "editorHoverWidget.background": getCSSVar('--popover'),
      "editorHoverWidget.border": getCSSVar('--border'),
      "input.background": getCSSVar('--input'),
      "input.border": getCSSVar('--border'),
      "dropdown.background": getCSSVar('--background'),
      "dropdown.border": getCSSVar('--border'),
      // Diff editor colors - use theme colors, no borders
      // Use semantic success (green) for insertions so they stay green across color schemes.
      "diffEditor.insertedTextBackground": getCSSVarWithAlpha('--success', 0.22),
      "diffEditor.removedTextBackground": getCSSVarWithAlpha('--destructive', 0.2),
      "diffEditor.insertedLineBackground": getCSSVarWithAlpha('--success', 0.16),
      "diffEditor.removedLineBackground": getCSSVarWithAlpha('--destructive', 0.15),
      "diffEditor.border": "#00000000",
      "diffEditor.insertedTextBorder": "#00000000",
      "diffEditor.removedTextBorder": "#00000000",
      "diffEditor.unchangedCodeBackground": "#00000000",
    },
  });

  monaco.editor.defineTheme("reliant-light", {
    base: "vs",
    inherit: true,
    rules: [...CEL_LIGHT_TOKEN_RULES],
    colors: {
      "editor.lineHighlightBackground": "#f3f3f3",
      "editorLineNumber.foreground": "#237893",
      "editorLineNumber.activeForeground": "#0b216f",
      "editor.selectionBackground": "#add6ff",
      "editor.inactiveSelectionBackground": "#e5ebf1",
      "editorWidget.background": getCSSVar('--popover'),
      "editorWidget.border": getCSSVar('--border'),
      "editorSuggestWidget.background": getCSSVar('--popover'),
      "editorSuggestWidget.border": getCSSVar('--border'),
      "editorHoverWidget.background": getCSSVar('--popover'),
      "editorHoverWidget.border": getCSSVar('--border'),
      "input.background": getCSSVar('--input'),
      "input.border": getCSSVar('--border'),
      "dropdown.background": getCSSVar('--background'),
      "dropdown.border": getCSSVar('--border'),
      // Diff editor colors - use theme colors, no borders
      // Use semantic success (green) for insertions so they stay green across color schemes.
      "diffEditor.insertedTextBackground": getCSSVarWithAlpha('--success', 0.22),
      "diffEditor.removedTextBackground": getCSSVarWithAlpha('--destructive', 0.2),
      "diffEditor.insertedLineBackground": getCSSVarWithAlpha('--success', 0.16),
      "diffEditor.removedLineBackground": getCSSVarWithAlpha('--destructive', 0.15),
      "diffEditor.border": "#00000000",
      "diffEditor.insertedTextBorder": "#00000000",
      "diffEditor.removedTextBorder": "#00000000",
      "diffEditor.unchangedCodeBackground": "#00000000",
    },
  });

  // Also define 'reliant' as an alias to the current mode for backwards compatibility
  monaco.editor.defineTheme("reliant", {
    base: isDarkMode ? "vs-dark" : "vs",
    inherit: true,
    rules: [...(isDarkMode ? CEL_DARK_TOKEN_RULES : CEL_LIGHT_TOKEN_RULES)],
    colors: {
      "editor.lineHighlightBackground": isDarkMode ? "#2a2d2e" : "#f3f3f3",
      "editorLineNumber.foreground": isDarkMode ? "#858585" : "#237893",
      "editorLineNumber.activeForeground": isDarkMode ? "#c6c6c6" : "#0b216f",
      "editor.selectionBackground": isDarkMode ? "#264f78" : "#add6ff",
      "editor.inactiveSelectionBackground": isDarkMode ? "#1a3a52" : "#e5ebf1",
      "editorWidget.background": getCSSVar('--popover'),
      "editorWidget.border": getCSSVar('--border'),
      "editorSuggestWidget.background": getCSSVar('--popover'),
      "editorSuggestWidget.border": getCSSVar('--border'),
      "editorHoverWidget.background": getCSSVar('--popover'),
      "editorHoverWidget.border": getCSSVar('--border'),
      "input.background": getCSSVar('--input'),
      "input.border": getCSSVar('--border'),
      "dropdown.background": getCSSVar('--background'),
      "dropdown.border": getCSSVar('--border'),
      // Diff editor colors - use theme colors, no borders
      // Use semantic success (green) for insertions so they stay green across color schemes.
      "diffEditor.insertedTextBackground": getCSSVarWithAlpha('--success', 0.22),
      "diffEditor.removedTextBackground": getCSSVarWithAlpha('--destructive', 0.2),
      "diffEditor.insertedLineBackground": getCSSVarWithAlpha('--success', 0.16),
      "diffEditor.removedLineBackground": getCSSVarWithAlpha('--destructive', 0.15),
      "diffEditor.border": "#00000000",
      "diffEditor.insertedTextBorder": "#00000000",
      "diffEditor.removedTextBorder": "#00000000",
      "diffEditor.unchangedCodeBackground": "#00000000",
    },
  });
}

/**
 * Gets the current theme name based on dark/light mode
 */
export function getCurrentMonacoTheme(): string {
  const isDarkMode = document.documentElement.classList.contains("dark");
  return isDarkMode ? "reliant-dark" : "reliant-light";
}

/**
 * Gets the language identifier for Monaco based on file extension
 */
export function getMonacoLanguage(filename: string | undefined): string {
  if (!filename) return "plaintext";
  const ext = filename.split(".").pop()?.toLowerCase() || "";
  const langMap: Record<string, string> = {
    ts: "typescript",
    tsx: "typescript",
    js: "javascript",
    jsx: "javascript",
    py: "python",
    go: "go",
    rs: "rust",
    java: "java",
    cpp: "cpp",
    c: "c",
    cs: "csharp",
    rb: "ruby",
    php: "php",
    swift: "swift",
    kt: "kotlin",
    scala: "scala",
    sh: "shell",
    bash: "shell",
    zsh: "shell",
    yaml: "yaml",
    yml: "yaml",
    json: "json",
    xml: "xml",
    html: "html",
    css: "css",
    scss: "scss",
    sass: "scss",
    md: "markdown",
    sql: "sql",
    graphql: "graphql",
    vue: "vue",
    svelte: "svelte",
  };
  return langMap[ext] || "plaintext";
}
