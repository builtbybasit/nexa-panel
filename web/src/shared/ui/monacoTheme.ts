import * as monaco from 'monaco-editor'

/**
 * Monaco theme derived from the app's design tokens (`web/src/styles/main.css`)
 * so the editor chrome matches the surrounding navy + teal UI instead of
 * Monaco's default warm-gray `vs-dark`. Registered once as a module side effect.
 *
 * Syntax token colors are inherited from `vs-dark` (well-tuned already) — only
 * the editor surface, gutter, cursor, selection, widgets, and diff highlights
 * are re-skinned to the app palette.
 */
export const NEXA_MONACO_THEME = 'nexa-dark'

monaco.editor.defineTheme(NEXA_MONACO_THEME, {
  base: 'vs-dark',
  inherit: true,
  rules: [{ token: 'comment', foreground: '64789b', fontStyle: 'italic' }],
  colors: {
    'editor.background': '#0b1422', // --color-surface
    'editor.foreground': '#e9eff9', // --color-ink
    'editorGutter.background': '#0b1422',
    'editorLineNumber.foreground': '#3a4a63',
    'editorLineNumber.activeForeground': '#93a5c1', // --color-ink-secondary
    'editorCursor.foreground': '#2dd4bf', // --color-accent-400
    'editor.selectionBackground': '#14b8a63a', // --color-accent-500 @ ~23%
    'editor.inactiveSelectionBackground': '#14b8a625',
    'editor.lineHighlightBackground': '#93a5c112',
    'editor.lineHighlightBorder': '#00000000',
    'editorIndentGuide.background1': '#94aac81f',
    'editorIndentGuide.activeBackground1': '#94aac83a',
    'editorWhitespace.foreground': '#93a5c11f',
    'editorWidget.background': '#0b1422',
    'editorWidget.border': '#94aac81f',
    'input.background': '#060c16', // --color-canvas
    'dropdown.background': '#0b1422',
    'scrollbarSlider.background': '#94aac81f',
    'scrollbarSlider.hoverBackground': '#94aac82e',
    'scrollbarSlider.activeBackground': '#94aac83a',
    'minimap.background': '#0b1422',
    'diffEditor.insertedTextBackground': '#2dd4bf22',
    'diffEditor.removedTextBackground': '#f43f5e22',
    'diffEditor.insertedLineBackground': '#2dd4bf14',
    'diffEditor.removedLineBackground': '#f43f5e14',
  },
})
