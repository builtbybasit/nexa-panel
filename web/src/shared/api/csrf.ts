/**
 * Double-submit CSRF token for the shared request helper.
 *
 * The server issues `nexa_csrf` alongside the session cookie and stores only its
 * hash against the session row. It is readable by script on purpose: echoing it
 * back in a header is what a cross-site request cannot do, which is what makes
 * the double submit a defence rather than a formality.
 */
const csrfCookieName = 'nexa_csrf'

const csrfHeaderName = 'X-CSRF-Token'

const safeMethods = new Set(['GET', 'HEAD', 'OPTIONS'])

function readCsrfToken(): string | undefined {
  if (typeof document === 'undefined') return undefined
  for (const entry of document.cookie.split(';')) {
    const separator = entry.indexOf('=')
    if (separator < 0) continue
    if (entry.slice(0, separator).trim() !== csrfCookieName) continue
    return decodeURIComponent(entry.slice(separator + 1).trim()) || undefined
  }
  return undefined
}

/**
 * Header to attach for `method`. Safe methods get nothing: the server does not
 * check them, and sending the token on reads would only widen its exposure.
 */
export function csrfHeaders(method?: string): Record<string, string> {
  if (safeMethods.has((method ?? 'GET').toUpperCase())) return {}
  const token = readCsrfToken()
  return token ? { [csrfHeaderName]: token } : {}
}
