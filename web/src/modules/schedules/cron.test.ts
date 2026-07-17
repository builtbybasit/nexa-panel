import { describe, expect, it } from 'vitest'

import { describe as describeExpression, nextRuns, normalize, parse } from './cron'

describe('parse', () => {
  it('parses a weekly expression', () => {
    const schedule = parse('0 0 * * 0')
    expect(schedule.minute.values).toEqual([0])
    expect(schedule.hour.values).toEqual([0])
    expect(schedule.dayOfMonth.any).toBe(true)
    expect(schedule.month.any).toBe(true)
    expect(schedule.dayOfWeek).toMatchObject({ values: [0], any: false })
  })

  it('expands wildcard steps', () => {
    const schedule = parse('*/15 * * * *')
    expect(schedule.minute.values).toEqual([0, 15, 30, 45])
    expect(schedule.minute.step).toBe(15)
    expect(schedule.minute.any).toBe(false)
  })

  it('resolves month and day names case-insensitively', () => {
    const schedule = parse('0 3 1 JAN MON')
    expect(schedule.month.values).toEqual([1])
    expect(schedule.dayOfWeek.values).toEqual([1])
    expect(parse('0 3 1 jan mon')).toEqual(schedule)
  })

  it('expands lists and ranges', () => {
    const schedule = parse('0,30 8-18 * * 1-5')
    expect(schedule.minute.values).toEqual([0, 30])
    expect(schedule.hour.values).toEqual([8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18])
    expect(schedule.dayOfWeek.values).toEqual([1, 2, 3, 4, 5])
  })

  it('expands range steps and name ranges', () => {
    expect(parse('0 0-23/6 * * *').hour.values).toEqual([0, 6, 12, 18])
    expect(parse('0 0 * jan,jul mon-fri').month.values).toEqual([1, 7])
    expect(parse('0 0 * jan,jul mon-fri').dayOfWeek.values).toEqual([1, 2, 3, 4, 5])
  })

  it('normalizes day-of-week 7 to Sunday', () => {
    expect(parse('0 0 * * 7').dayOfWeek.values).toEqual([0])
    expect(parse('0 0 * * 5-7').dayOfWeek.values).toEqual([0, 5, 6])
  })

  it('rejects out-of-range values with a precise message', () => {
    expect(() => parse('99 * * * *')).toThrow('minute must be 0-59, got 99')
    expect(() => parse('0 24 * * *')).toThrow('hour must be 0-23, got 24')
    expect(() => parse('0 0 0 * *')).toThrow('day-of-month must be 1-31, got 0')
    expect(() => parse('0 0 * 13 *')).toThrow('month must be 1-12, got 13')
    expect(() => parse('0 0 * * 8')).toThrow('day-of-week must be 0-7, got 8')
  })

  it('rejects the wrong number of fields', () => {
    expect(() => parse('0 0 * * * *')).toThrow('expected 5 fields (minute hour day-of-month month day-of-week), got 6')
    expect(() => parse('0 0 *')).toThrow('got 3')
    expect(() => parse('   ')).toThrow('the expression is empty')
  })

  it('rejects unknown names', () => {
    expect(() => parse('0 0 * FOO *')).toThrow("month has an invalid value 'FOO'")
    expect(() => parse('0 0 * * FUNDAY')).toThrow("day-of-week has an invalid value 'FUNDAY'")
  })

  it('rejects malformed steps, ranges, and lists', () => {
    expect(() => parse('*/0 * * * *')).toThrow("minute step must be a positive number, got '0'")
    expect(() => parse('5/2 * * * *')).toThrow("minute step is only allowed after * or a range, got '5/2'")
    expect(() => parse('30-10 * * * *')).toThrow('minute range must run low to high, got 30-10')
    expect(() => parse('1-2-3 * * * *')).toThrow("minute has an invalid value '1-2-3'")
    expect(() => parse('1, * * * *')).toThrow('minute has an empty list item')
  })

  it('rejects steps above the field maximum, matching the server validator', () => {
    expect(() => parse('*/120 * * * *')).toThrow('minute step must be at most 59, got 120')
    expect(() => parse('0 */24 * * *')).toThrow('hour step must be at most 23, got 24')
    expect(parse('*/59 * * * *').minute.step).toBe(59)
  })
})

describe('normalize', () => {
  it('rewrites month and day names as numbers for the server', () => {
    expect(normalize('0 3 1 JAN MON')).toBe('0 3 1 1 1')
    expect(normalize('0 0 * jan,jul mon-fri')).toBe('0 0 * 1,7 1-5')
    expect(normalize('0 9 * * MON-FRI')).toBe('0 9 * * 1-5')
  })

  it('leaves numeric expressions untouched', () => {
    expect(normalize('*/15 8-18 1,15 * 0')).toBe('*/15 8-18 1,15 * 0')
  })

  it('throws on invalid expressions', () => {
    expect(() => normalize('0 0 * FOO *')).toThrow("month has an invalid value 'FOO'")
  })
})

describe('describe', () => {
  it.each([
    ['* * * * *', 'Every minute'],
    ['*/15 * * * *', 'Every 15 minutes'],
    ['0 * * * *', 'Every hour'],
    ['0 */6 * * *', 'Every 6 hours'],
    ['15 * * * *', 'At minute 15 past every hour'],
    ['0 0 * * *', 'At 00:00'],
    ['0 0,12 * * *', 'At 00:00 and 12:00'],
    ['0 0 * * 0', 'At 00:00 on Sunday'],
    ['0 9 * * MON-FRI', 'At 09:00 on Monday through Friday'],
    ['0 0 1 * *', 'At 00:00 on day 1 of the month'],
    ['0 0 1,15 * *', 'At 00:00 on days 1 and 15 of the month'],
    ['0 3 1 JAN MON', 'At 03:00 on day 1 of the month or on Monday in January'],
  ])('describes %s as "%s"', (expression, sentence) => {
    expect(describeExpression(expression)).toBe(sentence)
  })
})

describe('nextRuns', () => {
  it('projects interval runs within the hour', () => {
    expect(nextRuns('*/15 * * * *', new Date(2026, 0, 1, 10, 7), 3)).toEqual([
      new Date(2026, 0, 1, 10, 15),
      new Date(2026, 0, 1, 10, 30),
      new Date(2026, 0, 1, 10, 45),
    ])
  })

  it('returns runs strictly after the from date', () => {
    expect(nextRuns('*/15 * * * *', new Date(2026, 0, 1, 10, 15), 1)).toEqual([new Date(2026, 0, 1, 10, 30)])
  })

  it('rolls over month boundaries', () => {
    expect(nextRuns('0 0 1 * *', new Date(2026, 0, 31, 23, 30), 3)).toEqual([
      new Date(2026, 1, 1, 0, 0),
      new Date(2026, 2, 1, 0, 0),
      new Date(2026, 3, 1, 0, 0),
    ])
  })

  it('finds the next weekday match', () => {
    // 2026-07-16 is a Thursday; the following Sundays are Jul 19, Jul 26, Aug 2.
    expect(nextRuns('0 0 * * 0', new Date(2026, 6, 16, 12, 0), 3)).toEqual([
      new Date(2026, 6, 19, 0, 0),
      new Date(2026, 6, 26, 0, 0),
      new Date(2026, 7, 2, 0, 0),
    ])
  })

  it('treats restricted day-of-month and day-of-week as either-matches, across a year boundary', () => {
    // Jan 1 2027 is a Friday; the first Mondays of January 2027 are Jan 4 and Jan 11.
    expect(nextRuns('0 3 1 JAN MON', new Date(2026, 11, 15, 0, 0), 3)).toEqual([
      new Date(2027, 0, 1, 3, 0),
      new Date(2027, 0, 4, 3, 0),
      new Date(2027, 0, 11, 3, 0),
    ])
  })

  it('skips to the next leap year for Feb 29', () => {
    expect(nextRuns('0 0 29 2 *', new Date(2026, 2, 1), 1)).toEqual([new Date(2028, 1, 29, 0, 0)])
  })

  it('gives up on impossible schedules instead of looping forever', () => {
    expect(nextRuns('0 0 31 2 *', new Date(2026, 0, 1), 1)).toEqual([])
  })
})
