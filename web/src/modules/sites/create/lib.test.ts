import { describe, expect, it } from 'vitest'

import { DB_NAME_RULE, deriveDbIdentifier, deriveSlug, sitePreviews, validateDomain } from './lib'

describe('deriveSlug', () => {
  it('slugs the display name and falls back to the domain', () => {
    expect(deriveSlug('Customer Portal', 'portal.example.com', new Set())).toBe('customer-portal')
    expect(deriveSlug('', 'portal.example.com', new Set())).toBe('portal-example-com')
    expect(deriveSlug('', '', new Set())).toBe('site')
  })

  it('never collides with existing slugs', () => {
    const taken = new Set(['shop', 'shop-2'])
    expect(deriveSlug('Shop', '', taken)).toBe('shop-3')
  })

  it('starts with a letter even for numeric names', () => {
    expect(deriveSlug('42', '', new Set())).toMatch(/^[a-z]/)
  })
})

describe('deriveDbIdentifier', () => {
  it('converts a slug into a valid identifier for both engines', () => {
    const id = deriveDbIdentifier('example-com', new Set())
    expect(id).toBe('example_com')
    expect(id).toMatch(DB_NAME_RULE)
  })

  it('suffixes to stay unique', () => {
    expect(deriveDbIdentifier('example-com', new Set(['example_com']))).toBe('example_com_2')
  })

  it('produces a valid identifier from hostile input', () => {
    for (const slug of ['1shop', 'a', '---', 'a-very-long-slug-name-well-beyond-the-identifier-cap']) {
      const id = deriveDbIdentifier(slug, new Set())
      expect(id).toMatch(DB_NAME_RULE)
      expect(id.length).toBeLessThanOrEqual(24)
    }
  })
})

describe('validateDomain', () => {
  it('accepts a plain hostname', () => {
    expect(validateDomain('portal.example.com', [])).toBe('')
  })

  it('rejects empty, malformed, and duplicate domains', () => {
    expect(validateDomain('', [])).not.toBe('')
    expect(validateDomain('not a domain', [])).not.toBe('')
    expect(validateDomain('single-label', [])).not.toBe('')
    expect(validateDomain('Portal.Example.com', ['portal.example.com'])).not.toBe('')
  })
})

describe('sitePreviews', () => {
  it('mirrors the backend derivations', () => {
    expect(sitePreviews('example-com')).toEqual({
      unixUser: 'nexa_example_com',
      rootPath: '/srv/nexa/sites/example-com',
      socketPath: '/run/php/nexa-example-com.sock',
    })
  })
})
