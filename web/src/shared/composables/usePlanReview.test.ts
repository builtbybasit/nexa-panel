import { effectScope } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import { usePlanReview } from './usePlanReview'

describe('usePlanReview', () => {
  it('loads either database engine through one adapter and normalizes plan warnings', async () => {
    const loadPlan = vi.fn().mockResolvedValue({
      plan: {
        operation: 'database.restore',
        agentPlan: { steps: ['Stop connections', 'Restore data'], warnings: null, interruption: true },
      },
      expiresAt: '2026-07-19T12:00:00Z',
    })
    const applyPlan = vi.fn()
    const scope = effectScope()
    const review = scope.run(() =>
      usePlanReview({
        loadPlan,
        applyPlan,
        refresh: vi.fn(),
        canApply: () => false,
        isDestructive: (target, plan) => target.type === 'restore-points' && plan.operation.includes('restore'),
      }),
    )
    if (!review) throw new Error('plan review scope was not created')

    try {
      await review.open({ type: 'restore-points', id: 'restore-1', label: 'Restore production' })

      expect(review.dialogOpen.value).toBe(true)
      expect(review.warnings.value).toEqual(['Active connections are terminated while this plan is applied.'])
      expect(review.dialogProps.value).toEqual({ steps: ['Stop connections', 'Restore data'], expiresAt: '2026-07-19T12:00:00Z' })
      expect(review.isDestructive.value).toBe(true)

      await review.apply()
      expect(applyPlan).not.toHaveBeenCalled()
    } finally {
      scope.stop()
    }
  })
})
