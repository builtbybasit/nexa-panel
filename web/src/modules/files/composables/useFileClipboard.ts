import { computed, ref, watch, type Ref } from 'vue'

import { copyEntry, moveEntry, type FileEntry } from '../api'
import { countLabel, freeName, joinPath } from '../lib'

export type ClipboardMode = 'copy' | 'cut'

/** What "paste over an existing name" should do; chosen per paste, not stored. */
export type PasteStrategy = 'keep-both' | 'replace' | 'skip'

export interface FileClipboard {
  mode: ClipboardMode
  /** Directory the entries were taken from — paste resolves sources against it. */
  directory: string
  names: string[]
}

export interface PasteSummary {
  pasted: number
  skipped: number
  /** Destination names that had to dodge an existing entry. */
  renamed: string[]
  failures: string[]
}

interface Options {
  siteId: Ref<string>
  /** The directory on screen; always the paste destination. */
  path: Ref<string>
  /** Entries of `path`, so collisions are known before anything is sent. */
  entries: Ref<FileEntry[]>
  canMutateHere: Ref<boolean>
}

/**
 * Copy/cut/paste over the file listing, replacing destination path prompts.
 *
 * The destination is always the directory the user is looking at, which is what
 * makes this safe to do without a text field: its listing is already loaded, so
 * name collisions are detected client-side and resolved explicitly rather than
 * discovered as a server error (copy) or a silent overwrite (move).
 */
export function useFileClipboard({ siteId, path, entries, canMutateHere }: Options) {
  const clipboard = ref<FileClipboard>()
  const busy = ref(false)

  // Sources are site-relative, so a clipboard cannot survive a site switch.
  watch(siteId, () => {
    clipboard.value = undefined
  })

  function hold(mode: ClipboardMode, names: string[]) {
    if (!names.length) return
    clipboard.value = { mode, directory: path.value, names: [...names] }
  }

  const copy = (names: string[]) => hold('copy', names)
  const cut = (names: string[]) => hold('cut', names)
  const clear = () => {
    clipboard.value = undefined
  }

  const count = computed(() => clipboard.value?.names.length ?? 0)

  /** Names on the clipboard that already exist in the destination directory. */
  const conflicts = computed(() => {
    const board = clipboard.value
    if (!board) return [] as string[]
    const present = new Set(entries.value.map((entry) => entry.name))
    return board.names.filter((name) => present.has(name))
  })

  /** A cut is a no-op in its own directory; a copy there duplicates instead. */
  const isSourceDirectory = computed(() => clipboard.value?.directory === path.value)
  const canPaste = computed(
    () => Boolean(clipboard.value) && canMutateHere.value && !(clipboard.value?.mode === 'cut' && isSourceDirectory.value),
  )

  const label = computed(() => {
    const board = clipboard.value
    if (!board) return ''
    return `${countLabel(board.names.length)} ready to ${board.mode === 'cut' ? 'move' : 'copy'}`
  })

  const blockedReason = computed(() => {
    if (!clipboard.value || canPaste.value) return ''
    if (clipboard.value.mode === 'cut' && isSourceDirectory.value) return 'These items are already in this folder'
    return 'This folder is read-only'
  })

  /**
   * Runs the pending operation into the current directory, one entry at a time
   * so a single failure does not abandon the rest. Failures stay on the
   * clipboard to be retried; anything else — pasted or deliberately skipped —
   * leaves it, so a paste the user resolved always empties the tray.
   */
  async function paste(strategy: PasteStrategy): Promise<PasteSummary> {
    const summary: PasteSummary = { pasted: 0, skipped: 0, renamed: [], failures: [] }
    const board = clipboard.value
    if (!board || !canPaste.value || busy.value) return summary

    busy.value = true
    // Tracked locally so two pasted siblings cannot claim the same free name.
    const taken = new Set(entries.value.map((entry) => entry.name))
    const failed: string[] = []
    try {
      for (const name of board.names) {
        const collides = taken.has(name)
        // Copy has no overwrite on the wire, so replacing is a cut-only choice.
        const replacing = collides && strategy === 'replace' && board.mode === 'cut'
        if (collides && !replacing && strategy !== 'keep-both') {
          summary.skipped += 1
          continue
        }
        const target = replacing ? name : freeName(name, taken)
        const from = joinPath(board.directory, name)
        const to = joinPath(path.value, target)
        try {
          if (board.mode === 'copy') await copyEntry(siteId.value, { from, to })
          else await moveEntry(siteId.value, { from, to, overwrite: replacing })
          taken.add(target)
          summary.pasted += 1
          if (target !== name) summary.renamed.push(target)
        } catch (caught) {
          summary.failures.push(`${name}: ${caught instanceof Error ? caught.message : 'failed'}`)
          failed.push(name)
        }
      }
    } finally {
      busy.value = false
    }

    if (failed.length) clipboard.value = { ...board, names: failed }
    else clipboard.value = undefined
    return summary
  }

  return { clipboard, busy, count, conflicts, canPaste, label, blockedReason, copy, cut, clear, paste }
}
