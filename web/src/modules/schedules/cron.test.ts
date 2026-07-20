import { describe, expect, it } from 'vitest'

import { describe as describeExpression, nextRuns, normalize } from './cron'

describe('cron validation', () => {
  it.each([
    ['0 0 * * 0', '0 0 * * 0'],
    ['*/15 * * * *', '*/15 * * * *'],
    ['0 3 1 jan mon', '0 3 1 1 1'],
    ['0,30 8-18 * * 1-5', '0,30 8-18 * * 1-5'],
    ['0 0-23/6 * * *', '0 0-23/6 * * *'],
    ['0 0 * jan,jul mon-fri', '0 0 * 1,7 1-5'],
    ['0 0 * * 7', '0 0 * * 7'],
    ['0 0 * * 5-7', '0 0 * * 5-7'],
    ['*/59 * * * *', '*/59 * * * *'],
  ])('accepts %s', (expression, normalized) => {
    expect(normalize(expression)).toBe(normalized)
  })

  it.each([
    ['99 * * * *', 'minute must be 0-59, got 99'],
    ['0 24 * * *', 'hour must be 0-23, got 24'],
    ['0 0 0 * *', 'day-of-month must be 1-31, got 0'],
    ['0 0 * 13 *', 'month must be 1-12, got 13'],
    ['0 0 * * 8', 'day-of-week must be 0-7, got 8'],
    ['0 0 * * * *', 'expected 5 fields (minute hour day-of-month month day-of-week), got 6'],
    ['0 0 *', 'got 3'],
    ['   ', 'the expression is empty'],
    ['0 0 * FOO *', "month has an invalid value 'FOO'"],
    ['0 0 * * FUNDAY', "day-of-week has an invalid value 'FUNDAY'"],
    ['*/0 * * * *', "minute step must be a positive number, got '0'"],
    ['5/2 * * * *', "minute step is only allowed after * or a range, got '5/2'"],
    ['30-10 * * * *', 'minute range must run low to high, got 30-10'],
    ['1-2-3 * * * *', "minute has an invalid value '1-2-3'"],
    ['1, * * * *', 'minute has an empty list item'],
    ['*/120 * * * *', 'minute step must be at most 59, got 120'],
    ['0 */24 * * *', 'hour step must be at most 23, got 24'],
  ])('rejects %s', (expression, message) => {
    expect(() => normalize(expression)).toThrow(message)
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
    ['0 0 * * 7', 'At 00:00 on Sunday'],
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
