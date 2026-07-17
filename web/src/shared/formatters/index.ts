const dateTimeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

export function formatDateTime(timestamp: string): string {
  return dateTimeFormat.format(new Date(timestamp))
}

export function formatTime(timestamp: string): string {
  return new Date(timestamp).toLocaleTimeString()
}

export function formatBytes(bytes?: number): string {
  if (!bytes) return '—'
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GiB`
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`
  return `${Math.ceil(bytes / 1024)} KiB`
}

/**
 * Byte sizes where zero is a real measurement rather than a missing one.
 * formatBytes renders 0 as "—" because its callers only ever have a size once
 * the thing exists; a database that has simply never been measured is a
 * different statement from one measured at zero bytes, and the two must not
 * read the same.
 */
export function formatMeasuredBytes(bytes?: number | null): string {
  if (bytes === undefined || bytes === null) return '—'
  if (bytes === 0) return '0 KiB'
  return formatBytes(bytes)
}

/** `plan_ready` → `plan ready` */
export function humanize(value: string): string {
  return value.replaceAll('_', ' ')
}

/** `site.plan` → `Site · Plan` */
export function formatJobKind(kind: string): string {
  return kind
    .split('.')
    .map((part) => (part[0]?.toUpperCase() ?? '') + part.slice(1))
    .join(' · ')
}

export function daysUntil(timestamp?: string): number | undefined {
  if (!timestamp) return undefined
  return Math.ceil((new Date(timestamp).getTime() - Date.now()) / 86_400_000)
}
