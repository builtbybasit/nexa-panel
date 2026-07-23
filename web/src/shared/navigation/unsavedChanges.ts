type DirtyCheck = () => boolean

const checks = new Set<DirtyCheck>()

function hasUnsavedChanges(): boolean {
  return [...checks].some((check) => check())
}

function beforeUnload(event: BeforeUnloadEvent) {
  if (!hasUnsavedChanges()) return
  event.preventDefault()
  event.returnValue = ''
}

/** Registers one editor/form whose draft would be lost when it unmounts. */
export function registerUnsavedChanges(check: DirtyCheck): () => void {
  if (checks.size === 0 && typeof window !== 'undefined') window.addEventListener('beforeunload', beforeUnload)
  checks.add(check)
  return () => {
    checks.delete(check)
    if (checks.size === 0 && typeof window !== 'undefined') window.removeEventListener('beforeunload', beforeUnload)
  }
}

/** Asks once before a navigation/auth transition discards registered drafts. */
export function requestDiscardUnsavedChanges(): boolean {
  if (!hasUnsavedChanges()) return true
  if (typeof globalThis.confirm !== 'function') return false
  return globalThis.confirm('You have unsaved changes. Leave this page and discard them?')
}
