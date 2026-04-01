import { useState } from 'react';
import { MonacoViewer } from './MonacoViewer';
import { LightweightCodeViewer } from './LightweightCodeViewer';
import { LightweightDiffViewer } from './LightweightDiffViewer';
import { DiffEditor } from '@monaco-editor/react';
import { configureMonacoTheme } from '../../lib/monacoTheme';

/**
 * CodeViewerComparison - Side-by-side comparison of Monaco vs Lightweight viewers
 * 
 * This component helps you visually compare the two implementations
 * to ensure the lightweight version looks identical to Monaco.
 */

const sampleCode = `import { useState, useEffect } from 'react';
import { Button } from './ui/button';

interface Props {
  name: string;
  count?: number;
}

export function Counter({ name, count = 0 }: Props) {
  const [value, setValue] = useState(count);
  
  useEffect(() => {
    console.log('Counter mounted:', name);
    return () => console.log('Counter unmounted');
  }, [name]);
  
  const increment = () => setValue(v => v + 1);
  const decrement = () => setValue(v => v - 1);
  
  return (
    <div className="counter">
      <h2>{name}</h2>
      <p>Value: {value}</p>
      <Button onClick={increment}>+</Button>
      <Button onClick={decrement}>-</Button>
    </div>
  );
}`;

const originalCode = `function hello() {
  console.log("Hello World");
  return 42;
}`;

const modifiedCode = `function greet(name) {
  console.log("Hello " + name);
  const result = 100;
  return result;
}`;

export function CodeViewerComparison() {
  const [view, setView] = useState<'code' | 'diff'>('code');

  return (
    <div className="p-8 space-y-6 bg-background">
      <div className="space-y-4">
        <h1 className="text-2xl font-bold">Monaco vs Lightweight Comparison</h1>
        <p className="text-muted-foreground">
          Compare the visual appearance and performance of Monaco vs Prism-based viewers
        </p>
        
        <div className="flex gap-2">
          <button
            onClick={() => setView('code')}
            className={`px-4 py-2 rounded ${
              view === 'code' ? 'bg-primary text-primary-foreground' : 'bg-muted'
            }`}
          >
            Code Viewer
          </button>
          <button
            onClick={() => setView('diff')}
            className={`px-4 py-2 rounded ${
              view === 'diff' ? 'bg-primary text-primary-foreground' : 'bg-muted'
            }`}
          >
            Diff Viewer
          </button>
        </div>
      </div>

      {view === 'code' && (
        <div className="grid grid-cols-2 gap-4">
          {/* Monaco Viewer */}
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold">Monaco Viewer</h2>
              <span className="text-xs text-muted-foreground font-mono">~5-10MB bundle</span>
            </div>
            <MonacoViewer
              content={sampleCode}
              language="typescript"
              maxHeight={400}
            />
          </div>

          {/* Lightweight Viewer */}
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold">Lightweight Viewer (Prism)</h2>
              <span className="text-xs text-success font-mono">~50KB bundle ✨</span>
            </div>
            <LightweightCodeViewer
              content={sampleCode}
              language="typescript"
              maxHeight={400}
            />
          </div>
        </div>
      )}

      {view === 'diff' && (
        <div className="grid grid-cols-2 gap-4">
          {/* Monaco Diff */}
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold">Monaco Diff Editor</h2>
              <span className="text-xs text-muted-foreground font-mono">~5-10MB bundle</span>
            </div>
            <div className="border border-border rounded-md overflow-hidden">
              <DiffEditor
                original={originalCode}
                modified={modifiedCode}
                language="javascript"
                height="250px"
                theme={document.documentElement.classList.contains('dark') ? 'vs-dark' : 'vs-light'}
                options={{
                  readOnly: true,
                  renderSideBySide: true,
                  minimap: { enabled: false },
                  scrollBeyondLastLine: false,
                  fontSize: 11,
                  lineHeight: 16,
                  ignoreTrimWhitespace: false,
                }}
                beforeMount={configureMonacoTheme}
              />
            </div>
          </div>

          {/* Lightweight Diff */}
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-semibold">Lightweight Diff Viewer</h2>
              <span className="text-xs text-success font-mono">~10KB bundle ✨</span>
            </div>
            <LightweightDiffViewer
              original={originalCode}
              modified={modifiedCode}
              filename="example.js"
              maxHeight={250}
            />
          </div>
        </div>
      )}

      {/* Comparison Notes */}
      <div className="mt-8 p-4 border border-border rounded-lg bg-card">
        <h3 className="font-semibold mb-2">Comparison Notes</h3>
        <ul className="space-y-2 text-sm text-muted-foreground">
          <li>✅ <strong>Bundle Size:</strong> Lightweight is 100x smaller (50KB vs 5-10MB)</li>
          <li>✅ <strong>Load Time:</strong> Lightweight loads instantly, Monaco takes 500ms-2s</li>
          <li>✅ <strong>Colors:</strong> Both use the same VSCode color scheme</li>
          <li>✅ <strong>Theme Support:</strong> Both auto-switch with app theme</li>
          <li>✅ <strong>Line Numbers:</strong> Both show line numbers</li>
          <li>❌ <strong>Interactive Features:</strong> Monaco has auto-complete, go-to-definition (not needed for read-only)</li>
          <li>❌ <strong>Minimap:</strong> Monaco has minimap (not needed for small snippets)</li>
        </ul>
      </div>

      {/* Performance Stats */}
      <div className="mt-4 p-4 border border-border rounded-lg bg-card">
        <h3 className="font-semibold mb-2">Expected Performance Impact</h3>
        <div className="grid grid-cols-3 gap-4 text-sm">
          <div>
            <div className="text-muted-foreground">Initial Bundle</div>
            <div className="text-2xl font-bold text-destructive">-5MB</div>
          </div>
          <div>
            <div className="text-muted-foreground">Chat Load Time</div>
            <div className="text-2xl font-bold text-success">-500ms</div>
          </div>
          <div>
            <div className="text-muted-foreground">Time to Interactive</div>
            <div className="text-2xl font-bold text-success">4-6x faster</div>
          </div>
        </div>
      </div>
    </div>
  );
}
