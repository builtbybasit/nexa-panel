import * as monaco from 'monaco-editor'
// Paths omit the `esm/vs/` prefix on purpose: monaco's package `exports` map
// remaps `./X` to `./esm/vs/X.js`, so the old deep paths no longer resolve.
import cssWorker from 'monaco-editor/language/css/css.worker?worker'
import editorWorker from 'monaco-editor/editor/editor.worker?worker'
import htmlWorker from 'monaco-editor/language/html/html.worker?worker'
import jsonWorker from 'monaco-editor/language/json/json.worker?worker'
import tsWorker from 'monaco-editor/language/typescript/ts.worker?worker'

/**
 * Configures Monaco's web workers via Vite's `?worker` imports — no separate
 * build step. Imported for its side effect by every editor wrapper so the
 * (large) worker bundles are only pulled in once a file is actually opened.
 */
;(self as typeof self & { MonacoEnvironment?: monaco.Environment }).MonacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    if (label === 'json') return new jsonWorker()
    if (label === 'css' || label === 'scss' || label === 'less') return new cssWorker()
    if (label === 'html' || label === 'handlebars' || label === 'razor') return new htmlWorker()
    if (label === 'typescript' || label === 'javascript') return new tsWorker()
    return new editorWorker()
  },
}
