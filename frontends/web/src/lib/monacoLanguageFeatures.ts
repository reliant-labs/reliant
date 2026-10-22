/**
 * Monaco Language Features Service
 * 
 * Provides enhanced language features for Monaco Editor:
 * - Go to Definition (F12, Cmd/Ctrl+Click)
 * - Hover information
 * - Find References (future)
 * - Peek Definition (future)
 * 
 * Phase 1: Single-file navigation using Monaco's built-in TypeScript service
 * Phase 2: Multi-file awareness via virtual filesystem sync
 */

import type { Monaco } from '@monaco-editor/react';
import { logger } from './logger';

// Use Monaco's own types - these are available at runtime via the monaco instance
type IDisposable = { dispose(): void };

// Track which files are registered in Monaco's virtual filesystem
interface RegisteredFile {
  uri: string;
  version: number;
  content: string;
}

// Service configuration options
interface LanguageFeaturesConfig {
  enableDefinition: boolean;
  enableHover: boolean;
  enableReferences: boolean;
  enableValidation: boolean;
}

const DEFAULT_CONFIG: LanguageFeaturesConfig = {
  enableDefinition: true,
  enableHover: true,
  enableReferences: false, // Phase 3
  enableValidation: false, // Keep disabled for now to avoid noise
};

class MonacoLanguageFeaturesService {
  private static instance: MonacoLanguageFeaturesService;
  private monaco: Monaco | null = null;
  private config: LanguageFeaturesConfig = DEFAULT_CONFIG;
  private registeredFiles: Map<string, RegisteredFile> = new Map();
  private disposables: IDisposable[] = [];
  private initialized = false;

  private constructor() {}

  static getInstance(): MonacoLanguageFeaturesService {
    if (!MonacoLanguageFeaturesService.instance) {
      MonacoLanguageFeaturesService.instance = new MonacoLanguageFeaturesService();
    }
    return MonacoLanguageFeaturesService.instance;
  }

  /**
   * Initialize the language features service with Monaco instance
   */
  initialize(monaco: Monaco, config?: Partial<LanguageFeaturesConfig>): void {
    if (this.initialized && this.monaco === monaco) {
      logger.debug('[MonacoLanguageFeatures] Already initialized');
      return;
    }

    this.monaco = monaco;
    this.config = { ...DEFAULT_CONFIG, ...config };
    
    logger.info('[MonacoLanguageFeatures] Initializing with config:', this.config);

    // Configure TypeScript defaults for better language support
    this.configureTypeScriptDefaults();

    // Register language feature providers
    this.registerProviders();

    this.initialized = true;
    logger.info('[MonacoLanguageFeatures] Initialization complete');
  }

  /**
   * Configure TypeScript/JavaScript defaults for optimal language features
   */
  private configureTypeScriptDefaults(): void {
    if (!this.monaco) return;

    const tsDefaults = this.monaco.languages.typescript.typescriptDefaults;
    const jsDefaults = this.monaco.languages.typescript.javascriptDefaults;

    // Configure compiler options for modern TypeScript/React
    const compilerOptions = {
      target: this.monaco.languages.typescript.ScriptTarget.ESNext,
      lib: ['ESNext', 'DOM', 'DOM.Iterable'],
      allowNonTsExtensions: true,
      moduleResolution: this.monaco.languages.typescript.ModuleResolutionKind.NodeJs,
      module: this.monaco.languages.typescript.ModuleKind.ESNext,
      noEmit: true,
      esModuleInterop: true,
      jsx: this.monaco.languages.typescript.JsxEmit.ReactJSX,
      jsxImportSource: 'react',
      allowJs: true,
      checkJs: false,
      typeRoots: ['node_modules/@types'],
      skipLibCheck: true,
      // Permissive settings to avoid false positives
      strict: false,
      noImplicitAny: false,
      strictNullChecks: false,
      strictFunctionTypes: false,
      strictBindCallApply: false,
      strictPropertyInitialization: false,
      noImplicitThis: false,
      alwaysStrict: false,
      resolveJsonModule: true,
      isolatedModules: true,
      allowSyntheticDefaultImports: true,
      allowUmdGlobalAccess: true,
      suppressImplicitAnyIndexErrors: true,
      noUnusedLocals: false,
      noUnusedParameters: false,
    };

    tsDefaults.setCompilerOptions(compilerOptions);
    jsDefaults.setCompilerOptions({
      ...compilerOptions,
      allowJs: true,
      checkJs: false,
    });

    // Configure diagnostics - keep validation off for now but enable language service
    const diagnosticsOptions = {
      // For Phase 1, keep validation disabled to avoid noise
      // We still get Go to Definition from the language service
      noSemanticValidation: !this.config.enableValidation,
      noSyntaxValidation: !this.config.enableValidation,
      noSuggestionDiagnostics: true,
    };

    tsDefaults.setDiagnosticsOptions(diagnosticsOptions);
    jsDefaults.setDiagnosticsOptions(diagnosticsOptions);

    // Enable eager model sync for better language service performance
    tsDefaults.setEagerModelSync(true);
    jsDefaults.setEagerModelSync(true);

    logger.debug('[MonacoLanguageFeatures] TypeScript defaults configured');
  }

  /**
   * Register language feature providers
   */
  private registerProviders(): void {
    if (!this.monaco) return;

    // Clean up any existing providers
    this.dispose();

    // Note: Monaco's TypeScript language service already provides:
    // - Definition provider (F12 / Cmd+Click)
    // - Hover provider
    // - Completion provider
    // These are built-in and work automatically when:
    // 1. The file is in the virtual filesystem
    // 2. The language service is configured properly
    
    // For Phase 1, we rely on Monaco's built-in providers
    // They work for single-file navigation out of the box
    
    // We'll add custom providers in later phases for:
    // - Cross-file navigation (Phase 2)
    // - Find References (Phase 3)
    // - Peek Definition (Phase 3)

    logger.debug('[MonacoLanguageFeatures] Providers registered');
  }

  /**
   * Register a file in Monaco's virtual filesystem
   * This allows the TypeScript service to provide language features for this file
   */
  registerFile(filePath: string, content: string): void {
    if (!this.monaco) return;

    // Normalize path for Monaco URI
    const uri = this.pathToUri(filePath);
    
    const existing = this.registeredFiles.get(uri);
    const version = existing ? existing.version + 1 : 1;

    // Add to TypeScript's extraLib for type information
    const isTypeScript = /\.(ts|tsx)$/i.test(filePath);
    const isJavaScript = /\.(js|jsx)$/i.test(filePath);

    if (isTypeScript) {
      this.monaco.languages.typescript.typescriptDefaults.addExtraLib(content, uri);
    } else if (isJavaScript) {
      this.monaco.languages.typescript.javascriptDefaults.addExtraLib(content, uri);
    }

    this.registeredFiles.set(uri, { uri, version, content });
    logger.debug('[MonacoLanguageFeatures] Registered file:', uri);
  }

  /**
   * Unregister a file from Monaco's virtual filesystem
   */
  unregisterFile(filePath: string): void {
    const uri = this.pathToUri(filePath);
    this.registeredFiles.delete(uri);
    // Note: Monaco doesn't have a removeExtraLib, but we track what's registered
    logger.debug('[MonacoLanguageFeatures] Unregistered file:', uri);
  }

  /**
   * Clear all registered files
   */
  clearFiles(): void {
    this.registeredFiles.clear();
    logger.debug('[MonacoLanguageFeatures] Cleared all registered files');
  }

  /**
   * Convert file path to Monaco URI
   */
  private pathToUri(filePath: string): string {
    // Normalize path separators
    const normalized = filePath.replace(/\\/g, '/');
    // Ensure it starts with file:///
    if (normalized.startsWith('file:///')) {
      return normalized;
    }
    return `file:///${normalized.replace(/^\//, '')}`;
  }

  /**
   * Get current configuration
   */
  getConfig(): LanguageFeaturesConfig {
    return { ...this.config };
  }

  /**
   * Update configuration
   */
  updateConfig(config: Partial<LanguageFeaturesConfig>): void {
    this.config = { ...this.config, ...config };
    
    // Re-configure TypeScript defaults if validation setting changed
    if ('enableValidation' in config) {
      this.configureTypeScriptDefaults();
    }
    
    logger.info('[MonacoLanguageFeatures] Config updated:', this.config);
  }

  /**
   * Get list of registered files
   */
  getRegisteredFiles(): string[] {
    return Array.from(this.registeredFiles.keys());
  }

  /**
   * Check if a file is registered
   */
  isFileRegistered(filePath: string): boolean {
    return this.registeredFiles.has(this.pathToUri(filePath));
  }

  /**
   * Clean up resources
   */
  dispose(): void {
    this.disposables.forEach(d => d.dispose());
    this.disposables = [];
    logger.debug('[MonacoLanguageFeatures] Disposed');
  }

  /**
   * Full cleanup including files
   */
  cleanup(): void {
    this.dispose();
    this.clearFiles();
    this.initialized = false;
    this.monaco = null;
    logger.info('[MonacoLanguageFeatures] Full cleanup complete');
  }
}

// Export singleton instance
export const monacoLanguageFeatures = MonacoLanguageFeaturesService.getInstance();

/**
 * Hook to initialize language features when Monaco is ready
 */
export function initializeLanguageFeatures(
  monaco: Monaco,
  config?: Partial<LanguageFeaturesConfig>
): void {
  monacoLanguageFeatures.initialize(monaco, config);
}
