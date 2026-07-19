import { computed, ref } from 'vue'

import { useJobRunner } from '@/shared/composables/useJobRunner'
import { useToasts } from '@/shared/composables/useToasts'

import { applyPlan, getPlan, type Plan, type ResourceType } from '../api'

export interface PlanTarget {
  type: ResourceType
  id: string
  label: string
}

/**
 * The plan → review → apply loop.
 *
 * Both the databases list and the per-database detail page start plan-backed
 * operations, and the state machine is identical on each. Only the facts shown
 * beside the plan differ, so those stay with the caller — the list describes an
 * account or database, the detail page a grant or restore point.
 */
export function usePlanReview(refresh: () => Promise<void>) {
  const toasts = useToasts()
  const applyRunner = useJobRunner()

  const target = ref<PlanTarget>()
  const plan = ref<Plan>()
  const expiresAt = ref<string>()
  const dialogOpen = ref(false)
  const busy = ref(false)
  /** Row whose Review button is waiting on getPlan, so only that row spins. */
  const loadingId = ref<string>()

  async function open(resource: PlanTarget) {
    loadingId.value = resource.id
    try {
      const response = await getPlan(resource.type, resource.id)
      target.value = resource
      plan.value = response.plan
      expiresAt.value = response.expiresAt
      dialogOpen.value = true
    } catch (caught) {
      toasts.push({
        title: 'Could not load the plan',
        body: caught instanceof Error ? caught.message : 'The plan is not ready yet.',
        tone: 'danger',
      })
    } finally {
      loadingId.value = undefined
    }
  }

  async function regenerate() {
    const resource = target.value
    if (!resource) return
    busy.value = true
    try {
      const response = await getPlan(resource.type, resource.id)
      plan.value = response.plan
      expiresAt.value = response.expiresAt
    } catch (caught) {
      toasts.push({
        title: 'Could not regenerate the plan',
        body: caught instanceof Error ? caught.message : 'Try preparing the operation again.',
        tone: 'danger',
      })
    } finally {
      busy.value = false
    }
  }

  async function apply() {
    const resource = target.value
    if (!resource) return
    dialogOpen.value = false
    await applyRunner.run(async () => (await applyPlan(resource.type, resource.id)).id, {
      onSettled: refresh,
      successToast: 'Plan applied',
      failureMessage: 'Applying the plan failed',
    })
  }

  const warnings = computed(() => {
    const current = plan.value
    if (!current) return []
    // The agent omits warnings entirely for plans that have none (create
    // account/database, grants), so it arrives as null — guard the spread.
    const items = [...(current.agentPlan.warnings ?? [])]
    if (current.agentPlan.interruption) items.push('Active connections are terminated while this plan is applied.')
    return items
  })

  const dialogProps = computed(() => ({
    steps: plan.value?.agentPlan.steps ?? [],
    expiresAt: expiresAt.value,
  }))

  /** True when the pending plan destroys data, so the caller can gate it behind a confirmation. */
  const isRestore = computed(() => {
    const current = plan.value
    if (!current || target.value?.type !== 'restore-points') return false
    return current.operation.toLowerCase().includes('restore') || current.agentPlan.interruption
  })

  return { target, plan, dialogOpen, busy, loadingId, open, regenerate, apply, applyRunner, warnings, dialogProps, isRestore }
}
