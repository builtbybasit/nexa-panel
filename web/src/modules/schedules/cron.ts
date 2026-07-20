// Hand-written 5-field cron utility (minute hour day-of-month month day-of-week)
// powering the schedule previews: parse, plain-language describe, and next-run
// projection. Supports `*`, `*/step` (on `*` or ranges), comma lists, ranges,
// and case-insensitive month/day names. The server stays the validation
// authority when a task is planned.

interface CronField {
  /** Expanded values, sorted and de-duplicated (day-of-week normalized to 0-6). */
  values: number[]
  /** True when the raw field was exactly `*`. */
  any: boolean
  // Set when the raw field was exactly `*/N` (a bare wildcard step).
  step?: number
}

interface CronSchedule {
  minute: CronField
  hour: CronField
  dayOfMonth: CronField
  month: CronField
  dayOfWeek: CronField
}

interface FieldSpec {
  name: string
  min: number
  max: number
  names?: Record<string, number>
}

const MONTH_NAMES: Record<string, number> = {
  JAN: 1, FEB: 2, MAR: 3, APR: 4, MAY: 5, JUN: 6, JUL: 7, AUG: 8, SEP: 9, OCT: 10, NOV: 11, DEC: 12,
}

const DAY_NAMES: Record<string, number> = { SUN: 0, MON: 1, TUE: 2, WED: 3, THU: 4, FRI: 5, SAT: 6 }

// Day-of-week allows 7 as a Vixie-style alias for Sunday; values normalize to 0-6.
const FIELD_SPECS: FieldSpec[] = [
  { name: 'minute', min: 0, max: 59 },
  { name: 'hour', min: 0, max: 23 },
  { name: 'day-of-month', min: 1, max: 31 },
  { name: 'month', min: 1, max: 12, names: MONTH_NAMES },
  { name: 'day-of-week', min: 0, max: 7, names: DAY_NAMES },
]

function resolveValue(token: string, spec: FieldSpec): number {
  if (/^\d+$/.test(token)) {
    const value = Number(token)
    if (value < spec.min || value > spec.max) {
      throw new Error(`${spec.name} must be ${spec.min}-${spec.max}, got ${value}`)
    }
    return value
  }
  const named = spec.names?.[token.toUpperCase()]
  if (named === undefined) throw new Error(`${spec.name} has an invalid value '${token}'`)
  return named
}

function resolveStep(raw: string, spec: FieldSpec): number {
  if (!/^\d+$/.test(raw) || Number(raw) < 1) {
    throw new Error(`${spec.name} step must be a positive number, got '${raw}'`)
  }
  const step = Number(raw)
  // The server rejects steps above the field maximum; match it so the form
  // cannot preview an expression the server will refuse.
  if (step > spec.max) {
    throw new Error(`${spec.name} step must be at most ${spec.max}, got ${step}`)
  }
  return step
}

/** Expands one comma-separated part (`*`, `N`, `A-B`, optionally `/step`). */
function expandPart(part: string, spec: FieldSpec): number[] {
  const pieces = part.split('/')
  if (pieces.length > 2 || pieces[0] === undefined) {
    throw new Error(`${spec.name} has an invalid value '${part}'`)
  }
  const base = pieces[0]
  const step = pieces[1] === undefined ? 1 : resolveStep(pieces[1], spec)

  let start: number
  let end: number
  if (base === '*') {
    start = spec.min
    end = spec.max
  } else if (base.includes('-')) {
    const bounds = base.split('-')
    const low = bounds[0]
    const high = bounds[1]
    if (bounds.length !== 2 || !low || !high) {
      throw new Error(`${spec.name} has an invalid value '${part}'`)
    }
    start = resolveValue(low, spec)
    end = resolveValue(high, spec)
    if (start > end) throw new Error(`${spec.name} range must run low to high, got ${base}`)
  } else {
    if (pieces[1] !== undefined) {
      throw new Error(`${spec.name} step is only allowed after * or a range, got '${part}'`)
    }
    return [resolveValue(base, spec)]
  }

  const values: number[] = []
  for (let value = start; value <= end; value += step) values.push(value)
  return values
}

function parseField(raw: string, spec: FieldSpec): CronField {
  const collected = new Set<number>()
  for (const part of raw.split(',')) {
    if (!part) throw new Error(`${spec.name} has an empty list item`)
    for (const value of expandPart(part, spec)) {
      collected.add(spec.max === 7 ? value % 7 : value)
    }
  }
  const stepMatch = /^\*\/(\d+)$/.exec(raw)
  return {
    values: [...collected].sort((a, b) => a - b),
    any: raw === '*',
    ...(stepMatch ? { step: Number(stepMatch[1]) } : {}),
  }
}

/** Parses a 5-field expression into expanded fields; throws a precise Error otherwise. */
function parse(expression: string): CronSchedule {
  const trimmed = expression.trim()
  if (!trimmed) throw new Error('the expression is empty')
  const fields = trimmed.split(/\s+/)
  if (fields.length !== 5) {
    throw new Error(`expected 5 fields (minute hour day-of-month month day-of-week), got ${fields.length}`)
  }
  const parsed = fields.map((field, index) => parseField(field, FIELD_SPECS[index] as FieldSpec))
  return {
    minute: parsed[0] as CronField,
    hour: parsed[1] as CronField,
    dayOfMonth: parsed[2] as CronField,
    month: parsed[3] as CronField,
    dayOfWeek: parsed[4] as CronField,
  }
}

/**
 * Rewrites month/day names (JAN, MON-FRI, …) as the plain numbers the server's
 * validator requires, leaving everything else untouched. Throws (via parse)
 * when the expression is invalid.
 */
export function normalize(expression: string): string {
  parse(expression)
  const fields = expression.trim().split(/\s+/)
  const nameMaps: (Record<string, number> | undefined)[] = FIELD_SPECS.map((spec) => spec.names)
  return fields
    .map((field, index) => {
      const names = nameMaps[index]
      if (!names) return field
      return field.replace(/[A-Za-z]+/g, (token) => String(names[token.toUpperCase()] ?? token))
    })
    .join(' ')
}

const MONTH_LABELS = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
]

const DAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

function joinAnd(items: string[]): string {
  if (items.length <= 1) return items[0] ?? ''
  if (items.length === 2) return `${items[0]} and ${items[1]}`
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
}

const pad = (value: number) => String(value).padStart(2, '0')

function timePhrase(schedule: CronSchedule): string {
  const minute = schedule.minute
  const hour = schedule.hour

  // Recurring minutes ('* ...' or '*/N ...') read as an interval.
  const interval = minute.any ? 'every minute' : minute.step ? `every ${minute.step} minutes` : ''
  if (interval) {
    if (hour.any) return interval
    if (hour.step) return `${interval} past every ${hour.step} hours`
    return `${interval} past hour ${joinAnd(hour.values.map(String))}`
  }

  const atMidnight = minute.values.length === 1 && minute.values[0] === 0
  const minuteList = joinAnd(minute.values.map(String))
  if (hour.any) {
    if (atMidnight) return 'every hour'
    return `at minute ${minuteList} past every hour`
  }
  if (hour.step) {
    if (atMidnight) return `every ${hour.step} hours`
    return `at minute ${minuteList} past every ${hour.step} hours`
  }

  const times: string[] = []
  for (const h of hour.values) for (const m of minute.values) times.push(`${pad(h)}:${pad(m)}`)
  if (times.length <= 4) return `at ${joinAnd(times)}`
  return `at minute ${minuteList} past hour ${joinAnd(hour.values.map(String))}`
}

function dayOfMonthPhrase(field: CronField): string {
  if (field.any || field.values.length === 31) return ''
  if (field.step) return `every ${field.step} days`
  const days = field.values
  return `on day${days.length > 1 ? 's' : ''} ${joinAnd(days.map(String))} of the month`
}

function dayOfWeekPhrase(field: CronField): string {
  if (field.any || field.values.length === 7) return ''
  const values = field.values
  const first = values[0] as number
  const last = values[values.length - 1] as number
  if (values.length >= 3 && last - first === values.length - 1) {
    return `on ${DAY_LABELS[first]} through ${DAY_LABELS[last]}`
  }
  return `on ${joinAnd(values.map((value) => DAY_LABELS[value] as string))}`
}

function monthPhrase(field: CronField): string {
  if (field.any || field.values.length === 12) return ''
  if (field.step) return `every ${field.step} months`
  return `in ${joinAnd(field.values.map((value) => MONTH_LABELS[value - 1] as string))}`
}

/** Renders the expression as a plain sentence, e.g. 'At 00:00 on Sunday'. */
export function describe(expression: string): string {
  const schedule = parse(expression)
  const phrases = [timePhrase(schedule)]
  const dom = dayOfMonthPhrase(schedule.dayOfMonth)
  const dow = dayOfWeekPhrase(schedule.dayOfWeek)
  // Standard cron: when both day fields are restricted, either one matching fires.
  if (dom && dow) phrases.push(`${dom} or ${dow}`)
  else if (dom || dow) phrases.push(dom || dow)
  const month = monthPhrase(schedule.month)
  if (month) phrases.push(month)
  const sentence = phrases.join(' ')
  return sentence.charAt(0).toUpperCase() + sentence.slice(1)
}

function matchesDay(schedule: CronSchedule, date: Date): boolean {
  const domMatches = schedule.dayOfMonth.values.includes(date.getDate())
  const dowMatches = schedule.dayOfWeek.values.includes(date.getDay())
  if (!schedule.dayOfMonth.any && !schedule.dayOfWeek.any) return domMatches || dowMatches
  if (!schedule.dayOfMonth.any) return domMatches
  if (!schedule.dayOfWeek.any) return dowMatches
  return true
}

/** The next `count` run times strictly after `from`, in local time. */
export function nextRuns(expression: string, from: Date, count: number): Date[] {
  const schedule = parse(expression)
  const runs: Date[] = []
  if (count <= 0) return runs

  const cursor = new Date(from)
  cursor.setSeconds(0, 0)
  cursor.setMinutes(cursor.getMinutes() + 1)
  // Impossible schedules (e.g. day 31 in February only) stop at this horizon.
  const horizon = new Date(from)
  horizon.setFullYear(horizon.getFullYear() + 5)

  while (runs.length < count && cursor <= horizon) {
    if (!schedule.month.values.includes(cursor.getMonth() + 1)) {
      cursor.setMonth(cursor.getMonth() + 1, 1)
      cursor.setHours(0, 0, 0, 0)
      continue
    }
    if (!matchesDay(schedule, cursor)) {
      cursor.setDate(cursor.getDate() + 1)
      cursor.setHours(0, 0, 0, 0)
      continue
    }
    if (!schedule.hour.values.includes(cursor.getHours())) {
      cursor.setHours(cursor.getHours() + 1, 0, 0, 0)
      continue
    }
    if (!schedule.minute.values.includes(cursor.getMinutes())) {
      cursor.setMinutes(cursor.getMinutes() + 1)
      continue
    }
    runs.push(new Date(cursor))
    cursor.setMinutes(cursor.getMinutes() + 1)
  }
  return runs
}
