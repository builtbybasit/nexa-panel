import { describe, expect, it } from 'vitest'

import { featureModules } from './registry'

describe('feature module registry', () => {
  it('registers modules with unique identities', () => {
    const identities = featureModules.map((feature) => feature.id)

    expect(new Set(identities).size).toBe(identities.length)
    expect(identities).toContain('overview')
    expect(identities).toContain('system')
    expect(identities).toContain('jobs')
    expect(identities).toContain('nodeoperations')
    expect(identities).toContain('audit')
  })

  it('owns at least one route per module', () => {
    for (const feature of featureModules) {
      expect(feature.routes.length, `${feature.id} should own a route`).toBeGreaterThan(0)
      for (const route of feature.routes) {
        expect(route.meta?.moduleId).toBe(feature.id)
      }
    }
  })

  it('sorts navigable modules by their declared order', () => {
    const orders = featureModules
      .filter((feature) => feature.navigation)
      .map((feature) => feature.navigation?.order ?? 999)

    expect(orders).toEqual([...orders].sort((left, right) => left - right))
  })
})
