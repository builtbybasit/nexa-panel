/**
 * Shared JSON request helper for feature-module API clients.
 *
 * Sends same-origin credentials, negotiates JSON, and surfaces the server's
 * safe `message` field when a request fails. `errorPrefix` keeps failure text
 * attributable to the owning module (e.g. "PostgreSQL request").
 */
export async function apiRequest<T>(path: string, init?: RequestInit, errorPrefix = 'Request'): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  if (!response.ok) {
    const payload = (await response.json().catch(() => null)) as { message?: string } | null
    throw new Error(payload?.message ?? `${errorPrefix} failed with status ${response.status}`)
  }
  return (await response.json()) as T
}
