/**
 * The Monaco editor, from this build.
 *
 * Monaco used to be fetched at runtime from a CDN (jsDelivr's `min/vs` AMD
 * build). A blocked or slow CDN then meant no editor, and the app depended on
 * a third party's uptime for every desktop page load. This module is the
 * editor bundled from the `monaco-editor` package instead: its code, CSS,
 * fonts and web workers are all files of this build, served from our own
 * origin by the web host and by Electron's `app://` handler alike.
 *
 * It is several megabytes, so nothing may import it statically: the only
 * importer is `monacoManager`, through `import()`, which makes it a chunk of
 * its own that no page pays for until it wants an editor
 * (`monacoImports.guard.test.ts` holds that).
 *
 * Workers come from Vite's `?worker` imports: each is emitted as a file under
 * `assets/` and constructed from a same-origin URL, which the CSP's
 * `worker-src 'self'` allows and which resolves inside the packaged web root
 * under `app://bundle/`. The labels are Monaco's own: `getWorker` is asked for
 * one by language-service label, and everything else is the editor worker.
 */

import * as monaco from "monaco-editor";
import EditorWorker from "monaco-editor/editor/editor.worker?worker";
import CssWorker from "monaco-editor/languages/features/css/css.worker?worker";
import HtmlWorker from "monaco-editor/languages/features/html/html.worker?worker";
import JsonWorker from "monaco-editor/languages/features/json/json.worker?worker";
import TsWorker from "monaco-editor/languages/features/typescript/ts.worker?worker";

/** The worker Monaco asks for by `label`. */
export function workerFor(label: string): Worker {
  switch (label) {
    case "json":
      return new JsonWorker();
    case "css":
    case "scss":
    case "less":
      return new CssWorker();
    case "html":
    case "handlebars":
    case "razor":
      return new HtmlWorker();
    case "typescript":
    case "javascript":
      return new TsWorker();
    default:
      return new EditorWorker();
  }
}

// `self`, as Monaco's docs spell it. This module only runs on the page's main
// thread, where `self`, `window` and `globalThis` are one object, and Monaco's
// worker factory reads `globalThis.MonacoEnvironment`.
self.MonacoEnvironment = {
  getWorker: (_workerId: string, label: string): Worker => workerFor(label),
};

export { monaco };
