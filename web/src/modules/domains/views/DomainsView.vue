<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { listSites } from '@/modules/sites/api'
import { formatTime } from '@/shared/formatters'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppInput,
  AppSelect,
  EmptyState,
  FactList,
  FormField,
  JobProgress,
  PageHeader,
  ResourceRow,
  StatusPill,
} from '@/shared/ui'

import { activateDomain, createDomain, getDomainPlan, listDomains, type Domain, type DomainKind, type DomainPlan } from '../api'

const siteId = ref('')
const hostname = ref('')
const kind = ref<Exclude<DomainKind, 'primary'>>('subdomain')
const redirectTarget = ref('')

const selected = ref<Domain>()
const plan = ref<DomainPlan>()
const planExpiresAt = ref('')

const runner = useJobRunner()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites })
const domainsQuery = useQuery({ queryKey: ['domains'], queryFn: () => listDomains() })
const sites = computed(() => sitesQuery.data.value ?? [])
const domains = computed(() => domainsQuery.data.value ?? [])

const planFacts = computed(() =>
  plan.value
    ? [
        { label: 'Resolved addresses', value: plan.value.resolvedAddresses.join(', ') || 'No records' },
        { label: 'Attached hostnames', value: `${plan.value.nodePlan.site.routes?.length ?? 0} routes` },
        { label: 'Site', value: plan.value.nodePlan.site.primaryDomain },
      ]
    : [],
)

async function refreshSelected(domainId: string) {
  await domainsQuery.refetch()
  selected.value = domains.value.find((domain) => domain.id === domainId)
}

async function loadPlan(domain: Domain) {
  selected.value = domain
  plan.value = undefined
  if (domain.status !== 'plan_ready' && domain.status !== 'active') return
  try {
    const response = await getDomainPlan(domain.id)
    plan.value = response.plan
    planExpiresAt.value = response.expiresAt
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The routing plan could not be loaded.'
  }
}

async function submit() {
  await runner.run(
    async () => {
      const result = await createDomain({
        siteId: siteId.value,
        hostname: hostname.value,
        kind: kind.value,
        ...(kind.value === 'redirect' ? { redirectTarget: redirectTarget.value } : {}),
      })
      selected.value = result.domain
      return result.job.id
    },
    {
      onSettled: async () => {
        if (selected.value) await refreshSelected(selected.value.id)
      },
      onSuccess: async () => {
        if (selected.value) await loadPlan(selected.value)
      },
      failureMessage: 'The domain operation failed. Inspect Jobs for details.',
    },
  )
}

async function activate() {
  const domain = selected.value
  if (!domain) return
  await runner.run(async () => (await activateDomain(domain.id)).id, {
    onSettled: () => refreshSelected(domain.id),
    failureMessage: 'The domain activation failed. Inspect Jobs for details.',
  })
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Routing and preflight"
      title="Domains"
      description="Hostnames are globally unique on this node. Every change regenerates and validates the complete Nginx site as one atomic change."
    />

    <div class="grid gap-4 lg:grid-cols-[minmax(0,380px)_1fr]">
      <AppCard eyebrow="Attach hostname" title="New route">
        <form class="space-y-4" @submit.prevent="submit">
          <FormField label="Site">
            <AppSelect v-model="siteId" required>
              <option disabled value="">Select site</option>
              <option v-for="site in sites" :key="site.id" :value="site.id">{{ site.displayName }}</option>
            </AppSelect>
          </FormField>
          <FormField label="Kind">
            <AppSelect v-model="kind">
              <option value="subdomain">Subdomain</option>
              <option value="alias">Alias</option>
              <option value="redirect">Redirect</option>
            </AppSelect>
          </FormField>
          <FormField label="Hostname">
            <AppInput v-model="hostname" placeholder="www.example.com" required />
          </FormField>
          <FormField v-if="kind === 'redirect'" label="Redirect target hostname">
            <AppInput v-model="redirectTarget" placeholder="example.com" required />
          </FormField>

          <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
          <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />

          <AppButton variant="primary" type="submit" :loading="runner.busy.value" class="w-full">
            Create routing plan
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="Routing inventory" :title="`${domains.length} hostnames`" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="domains.length" class="space-y-1">
            <ResourceRow
              v-for="domain in domains"
              :key="domain.id"
              :title="domain.hostname"
              :subtitle="`${domain.kind}${domain.redirectTarget ? ` → ${domain.redirectTarget}` : ''}`"
              icon="globe"
              clickable
              @select="loadPlan(domain)"
            >
              <template #meta>
                <span class="hidden shrink-0 font-mono text-xs text-ink-muted sm:inline">
                  {{ domain.resolvedAddresses[0] ?? 'DNS pending' }}
                </span>
              </template>
              <template #status>
                <StatusPill :status="domain.status" />
              </template>
            </ResourceRow>
          </div>
          <EmptyState
            v-else
            icon="globe"
            title="No additional hostnames"
            description="Attach a subdomain, alias, or redirect to a managed site."
            class="m-2"
          />
        </div>
      </AppCard>
    </div>

    <AppCard v-if="selected && plan" eyebrow="DNS and Nginx plan" :title="selected.hostname">
      <template #actions>
        <StatusPill tone="warning" :label="`Expires ${formatTime(planExpiresAt)}`" :pulse="false" />
      </template>

      <div class="space-y-4">
        <AppAlert v-for="warning in plan.warnings" :key="warning" tone="warning">{{ warning }}</AppAlert>
        <FactList :facts="planFacts" />
        <div class="flex flex-wrap gap-2">
          <AppButton
            variant="primary"
            :loading="runner.busy.value"
            :disabled="selected.status === 'active'"
            @click="activate"
          >
            {{ selected.status === 'active' ? 'Active' : 'Approve and activate' }}
          </AppButton>
        </div>
      </div>
    </AppCard>
  </section>
</template>
