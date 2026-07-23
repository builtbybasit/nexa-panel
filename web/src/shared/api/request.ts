import { activityHeaders } from './activity'
import { csrfHeaders } from './csrf'
import { requestMFAStepUp } from './mfaStepUp'
import { handleUnauthorized } from './unauthorized'

/**
 * Shared JSON request helper for feature-module API clients.
 *
 * Sends same-origin credentials, negotiates JSON, attaches the double-submit
 * CSRF token on unsafe methods, marks traffic no user caused so the server's
 * idle timeout is not renewed by background polling, and surfaces the server's
 * safe `message` field when a request fails. `errorPrefix` keeps failure text
 * attributable to the owning module (e.g. "PostgreSQL request").
 */
interface ApiErrorPayload {
  code?: string
  message?: string
}

export interface ApiRequestOptions {
  errorPrefix?: string
  createError?: (message: string, status: number, code?: string) => Error
  retryAfterMFAStepUp?: boolean
  /** Authentication endpoints set this false because a 401 is an expected form error. */
  handleUnauthorized?: boolean
}

class ApiRequestError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message)
  }
}

export async function apiRequest<T>(
  path: string,
  init?: RequestInit,
  options: string | ApiRequestOptions = 'Request',
): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...csrfHeaders(init?.method),
      ...activityHeaders(),
      ...init?.headers,
    },
  })
  if (!response.ok) {
    const payload = (await response.json().catch(() => null)) as ApiErrorPayload | null
    const resolved = typeof options === 'string' ? { errorPrefix: options } : options
    const message = payload?.message ?? `${resolved.errorPrefix ?? 'Request'} failed with status ${response.status}`
    const error = resolved.createError?.(message, response.status, payload?.code) ?? new ApiRequestError(message, response.status, payload?.code)
    if (response.status === 401 && resolved.handleUnauthorized !== false) {
      await handleUnauthorized()
    }
    if (payload?.code === 'mfa_step_up_required' && resolved.retryAfterMFAStepUp !== false) {
      try {
        if (await requestMFAStepUp()) {
          return apiRequest<T>(path, init, { ...resolved, retryAfterMFAStepUp: false })
        }
      } catch {
        // Cancellation keeps the original server error, which is more useful
        // to the initiating flow than a UI-specific cancellation error.
      }
    }
    throw error
  }
  if (response.status === 204 || response.status === 205) return undefined as T

  const body = await response.text()
  if (!body.trim()) return undefined as T
  return JSON.parse(body) as T
}
