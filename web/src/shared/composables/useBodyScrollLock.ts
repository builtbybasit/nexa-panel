/**
 * Reference-counted body scroll lock shared by every overlay (dialogs, the
 * command palette, the mobile sidebar). A counter instead of per-caller
 * save/restore means stacked overlays can lock and release in any order —
 * the page scrolls again only once the last lock is released.
 */
let lockCount = 0

export function lockBodyScroll() {
  lockCount += 1
  if (lockCount === 1) document.body.style.overflow = 'hidden'
}

export function unlockBodyScroll() {
  if (lockCount === 0) return
  lockCount -= 1
  if (lockCount === 0) document.body.style.overflow = ''
}
