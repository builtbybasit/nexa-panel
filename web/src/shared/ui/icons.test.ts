import { CircleQuestionMark, Database, Square } from 'lucide-vue-next'
import { describe, expect, it } from 'vitest'

import { iconFor } from './icons'

describe('icon map', () => {
  it('resolves the finite application icon set without a runtime icon service', () => {
    expect(iconFor('database')).toBe(Database)
    expect(iconFor('stop')).toBe(Square)
    expect(iconFor('unknown-icon')).toBe(CircleQuestionMark)
  })
})
