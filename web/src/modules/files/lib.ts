import { formatBytes } from '@/shared/formatters'

import type { FileEntry } from './api'

/** Directories the server accepts mutations in; everything else is read-only. */
const writeZones = new Set(['public', 'private', 'tmp', 'backups'])

export const isWritablePath = (target: string) => writeZones.has(target.split('/')[0] ?? '')

export function joinPath(directory: string, name: string): string {
  return directory === '.' ? name : `${directory}/${name}`
}

export function parentOf(target: string): string {
  if (target === '.') return '.'
  const segments = target.split('/')
  return segments.length > 1 ? segments.slice(0, -1).join('/') : '.'
}

export function crumbsOf(target: string): { name: string; path: string }[] {
  if (target === '.') return []
  const segments = target.split('/')
  return segments.map((name, index) => ({ name, path: segments.slice(0, index + 1).join('/') }))
}

export const displayPathOf = (target: string) => (target === '.' ? '/' : `/${target}`)

// --- Entry presentation: icon, type label, size, octal access ---

const archiveExtensions = new Set(['zip', 'gz', 'tgz', 'tar', 'bz2', 'xz', 'rar', '7z'])
const imageExtensions = new Set(['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'ico', 'avif', 'bmp'])
const codeExtensions = new Set([
  'js', 'mjs', 'cjs', 'jsx', 'ts', 'tsx', 'json', 'html', 'htm', 'vue', 'xml', 'css', 'scss', 'less',
  'php', 'py', 'rb', 'go', 'rs', 'java', 'c', 'h', 'cpp', 'cs', 'sh', 'bash', 'yml', 'yaml', 'sql', 'lua',
])

function extensionOf(name: string): string {
  return name.includes('.') ? (name.split('.').pop() ?? '').toLowerCase() : ''
}

/** True for the archive shapes the server can extract. */
export function isArchiveName(name: string): boolean {
  return name.endsWith('.zip') || name.endsWith('.tar.gz') || name.endsWith('.tgz')
}

export function entryIcon(entry: FileEntry): string {
  if (entry.kind === 'dir') return 'folder'
  if (entry.kind === 'symlink') return 'external-link'
  if (entry.kind === 'other') return 'info'
  const ext = extensionOf(entry.name)
  if (isArchiveName(entry.name) || archiveExtensions.has(ext)) return 'archive'
  if (imageExtensions.has(ext)) return 'image'
  if (codeExtensions.has(ext)) return 'file-code-2'
  return 'file-text'
}

export function entryTypeLabel(entry: FileEntry): string {
  if (entry.kind === 'dir') return 'Folder'
  if (entry.kind === 'symlink') return 'Symlink'
  if (entry.kind === 'other') return 'Special'
  const ext = extensionOf(entry.name)
  if (isArchiveName(entry.name) || archiveExtensions.has(ext)) return 'Archive'
  if (imageExtensions.has(ext)) return 'Image'
  return ext && ext.length <= 4 ? ext.toUpperCase() : 'File'
}

export function entrySize(entry: FileEntry): string {
  if (entry.kind === 'dir') return '—'
  return entry.size === 0 ? '0 B' : formatBytes(entry.size)
}

/** Converts a symbolic permission string such as `rwxr-x---` to octal `750`. */
export function symbolicToOctal(mode: string): string {
  if (mode.length !== 9) return ''
  const digit = (triplet: string) =>
    (triplet[0] !== '-' ? 4 : 0) + (triplet[1] !== '-' ? 2 : 0) + (triplet[2] !== '-' ? 1 : 0)
  return `${digit(mode.slice(0, 3))}${digit(mode.slice(3, 6))}${digit(mode.slice(6, 9))}`
}

export const entryAccess = (entry: FileEntry) => symbolicToOctal(entry.mode) || entry.mode

export const countLabel = (count: number) => `${count} ${count === 1 ? 'item' : 'items'}`

// --- Names ---

/**
 * Splits a name at its first inner dot so multi-part extensions survive a
 * rename: `site.tar.gz` is stem `site` plus extension `.tar.gz`, while the
 * leading dot of `.htaccess` stays part of the stem.
 */
function splitName(name: string): { stem: string; ext: string } {
  const dot = name.indexOf('.', 1)
  return dot === -1 ? { stem: name, ext: '' } : { stem: name.slice(0, dot), ext: name.slice(dot) }
}

/**
 * The name itself when nothing claims it, otherwise the first free
 * `name (copy)`, `name (copy 2)`, … variant. Used to paste beside an existing
 * entry instead of overwriting it.
 */
export function freeName(name: string, taken: ReadonlySet<string>): string {
  if (!taken.has(name)) return name
  const { stem, ext } = splitName(name)
  for (let attempt = 1; ; attempt += 1) {
    const candidate = attempt === 1 ? `${stem} (copy)${ext}` : `${stem} (copy ${attempt})${ext}`
    if (!taken.has(candidate)) return candidate
  }
}

/** The three dialogs that only ever ask for a name inside the current folder. */
export type NameDialogKind = 'mkdir' | 'newfile' | 'rename'

/**
 * Everything the toolbar and the context menu can ask for. Both raise the same
 * event and the view resolves it against the current selection, so a right-click
 * inside a multi-selection acts on all of it, exactly like the toolbar does.
 */
export type FileAction =
  | 'open'
  | 'edit'
  | 'copy-path'
  | 'compute-size'
  | 'clipboard-copy'
  | 'clipboard-cut'
  | 'paste'
  | 'clipboard-clear'
  | 'mkdir'
  | 'newfile'
  | 'rename'
  | 'chmod'
  | 'extract'
  | 'archive'
  | 'delete'
  | 'upload'
  | 'clear-selection'

export function defaultArchiveName(): string {
  const stamp = new Date().toISOString().replace(/[-:]/g, '').slice(0, 15)
  return `archive-${stamp}.tar.gz`
}

/** Shared styling for the flat icon+label buttons in the file manager toolbar. */
export const toolbarButton =
  'inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-[13px] font-medium text-ink-secondary transition-colors hover:bg-white/[0.06] hover:text-ink disabled:cursor-not-allowed disabled:opacity-40'
