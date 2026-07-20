import { computed, ref, shallowRef } from 'vue'

import { useJobRunner } from './useJobRunner'
import { useToasts } from './useToasts'

export interface PlanTarget<ResourceType extends string = string> {
  type: ResourceType
  id: string
  label: string
}

export interface ReviewablePlan {
  operation: string
  agentPlan: {
    steps?: string[] | null
    warnings?: string[] | null
    interruption?: boolean
  }
}

export interface PlanReviewOptions<ResourceType extends string, Plan extends ReviewablePlan> {
  loadPlan: (resourceType: ResourceType, id: string) => Promise<{ plan: Plan; expiresAt: string }>
  applyPlan: (resourceType: ResourceType, id: string) => Promise<{ id: number }>
  refresh: () => Promise<void>
  canApply: () => boolean
  isDestructive?: (target: PlanTarget<ResourceType>, plan: Plan) => boolean
}

/** Owns the complete plan -> review -> apply state machine for any resource adapter. */
export function usePlanReview<ResourceType extends string, Plan extends ReviewablePlan>(
  options: PlanReviewOptions<ResourceType, Plan>,
) {
  const toasts = useToasts()
  const applyRunner = useJobRunner()

  const target = ref<PlanTarget<ResourceType>>()
  const plan = shallowRef<Plan>()
  const expiresAt = ref<string>()
  const dialogOpen = ref(false)
  const busy = ref(false)
  const loadingId = ref<string>()

  async function open(resource: PlanTarget<ResourceType>) {
    loadingId.value = resource.id
    try {
      const response = await options.loadPlan(resource.type, resource.id)
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
      const response = await options.loadPlan(resource.type, resource.id)
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
    if (!options.canApply()) {
      toasts.push({ title: 'Administrator approval required', body: 'Your role can review this plan but cannot apply it.', tone: 'warning' })
      return
    }
    dialogOpen.value = false
    await applyRunner.run(async () => (await options.applyPlan(resource.type, resource.id)).id, {
      onSettled: options.refresh,
      successToast: 'Plan applied',
      failureMessage: 'Applying the plan failed',
    })
  }

  const warnings = computed(() => {
    const current = plan.value
    if (!current) return []
    const items = [...(current.agentPlan.warnings ?? [])]
    if (current.agentPlan.interruption) items.push('Active connections are terminated while this plan is applied.')
    return items
  })

  const dialogProps = computed(() => ({
    steps: [...(plan.value?.agentPlan.steps ?? [])],
    expiresAt: expiresAt.value,
  }))

  const isDestructive = computed(() => {
    const resource = target.value
    const current = plan.value
    return resource !== undefined && current !== undefined && (options.isDestructive?.(resource, current) ?? false)
  })

  return { target, plan, dialogOpen, busy, loadingId, open, regenerate, apply, applyRunner, warnings, dialogProps, isDestructive }
}
