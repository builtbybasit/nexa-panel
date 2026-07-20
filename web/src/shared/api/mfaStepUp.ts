export type MFAStepUpHandler = () => Promise<void>

let activeHandler: MFAStepUpHandler | undefined
let pendingStepUp: Promise<boolean> | undefined

/**
 * Register the authenticated shell's MFA prompt. The request layer knows only
 * that verification completed; authenticator fields and identity state remain
 * owned by the identity module.
 */
export function registerMFAStepUpHandler(handler: MFAStepUpHandler): () => void {
  activeHandler = handler
  return () => {
    if (activeHandler === handler) activeHandler = undefined
  }
}

/** Coalesce simultaneous protected requests behind one verification prompt. */
export function requestMFAStepUp(): Promise<boolean> {
  if (!activeHandler) return Promise.resolve(false)
  if (!pendingStepUp) {
    pendingStepUp = activeHandler()
      .then(() => true)
      .finally(() => {
        pendingStepUp = undefined
      })
  }
  return pendingStepUp
}
