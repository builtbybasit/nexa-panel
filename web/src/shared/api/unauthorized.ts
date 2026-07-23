type UnauthorizedHandler = () => void | Promise<void>

let handler: UnauthorizedHandler | undefined
let pending: Promise<void> | undefined

/** Registers the application-owned response to an expired authenticated session. */
export function registerUnauthorizedHandler(next: UnauthorizedHandler): () => void {
  handler = next
  return () => {
    if (handler === next) handler = undefined
  }
}

/** Coalesces concurrent 401 responses so one expired session is cleared once. */
export function handleUnauthorized(): Promise<void> {
  if (!handler) return Promise.resolve()
  if (!pending) {
    pending = Promise.resolve(handler()).finally(() => {
      pending = undefined
    })
  }
  return pending
}
