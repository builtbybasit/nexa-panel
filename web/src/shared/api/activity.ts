/**
 * Background-traffic marker for the shared request helper.
 *
 * The server's idle timeout is supposed to end a session nobody is using, but
 * the SPA polls jobs, services and the system overview every few seconds for as
 * long as a tab is open, and every authenticated request slides the idle window
 * forward. Without a way to tell "someone is working" apart from "a tab was left
 * open", the timeout only ever fires on the absolute cap.
 *
 * The marker is derived here rather than passed in by callers because the polls
 * go through the very same query functions as the user-initiated reads; only the
 * presence of real input distinguishes them.
 *
 * It fails safe by construction: the header is sent ONLY when this code can
 * positively attribute a request to background traffic, so anything that does
 * not run it -- an older client, curl, the agent -- is treated as activity and
 * behaves exactly as it did before.
 */
const backgroundHeaderName = 'X-Nexa-Background'

/**
 * How long one interaction keeps counting as activity. Long enough that reading
 * a page between clicks is never mistaken for an abandoned tab, short enough
 * that the polls of an untouched tab stop renewing the session.
 */
const interactionWindowMs = 5 * 60_000

/** Capture phase, so input on any element counts even when it does not bubble. */
const interactionEvents = ['pointerdown', 'keydown', 'wheel', 'touchstart', 'scroll'] as const

// A freshly loaded page is the user's own doing, so start inside the window.
let lastInteractionAt = Date.now()

if (typeof document !== 'undefined') {
  for (const name of interactionEvents) {
    document.addEventListener(
      name,
      () => {
        lastInteractionAt = Date.now()
      },
      { capture: true, passive: true },
    )
  }
}

/** Header marking a request no person caused; empty while the user is present. */
export function activityHeaders(): Record<string, string> {
  if (typeof document === 'undefined') return {}
  return Date.now() - lastInteractionAt <= interactionWindowMs ? {} : { [backgroundHeaderName]: '1' }
}
