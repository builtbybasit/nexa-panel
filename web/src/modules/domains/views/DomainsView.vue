<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { listSites } from '@/modules/sites/api'
import { useIdentityStore } from '@/modules/identity/store'
import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppInput,
  EmptyState,
  FactList,
  FormField,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  PlanReviewDialog,
  ResourceRow,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'
import {
  Combobox,
  ComboboxAnchor,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxItemIndicator,
  ComboboxList,
  ComboboxTrigger,
} from '@/shared/ui/combobox'

import {
  activateDomain,
  createDomain,
  deleteDomain,
  getDomainPlan,
  listDomains,
  type Domain,
  type DomainKind,
  type DomainPlan,
} from '../api'

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('domains.write'))
const canApply = computed(() => identity.can('operations.apply'))
const runner = useJobRunner()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites })
const domainsQuery = useQuery({ queryKey: ['domains'], queryFn: () => listDomains() })
const sites = computed(() => sitesQuery.data.value ?? [])
const domains = computed(() => domainsQuery.data.value ?? [])

const siteFilter = ref('')
const filteredDomains = computed(() =>
  siteFilter.value ? domains.value.filter((domain) => domain.siteId === siteFilter.value) : domains.value,
)
const ALL_SITES = '__all__'
const siteFilterModel = computed({
  get: () => siteFilter.value || ALL_SITES,
  set: (value: string) => {
    siteFilter.value = value === ALL_SITES ? '' : value
  },
})
const { search, page, pageCount, items: pageItems, matching } = useCollection(() => filteredDomains.value, {
  searchText: (domain) => domain.hostname,
})

const selected = ref<Domain>()
const detailEl = ref<HTMLElement>()

const plan = ref<DomainPlan>()
const planExpiresAt = ref('')
const planOpen = ref(false)
const planLoading = ref(false)
const planError = ref('')

const removeOpen = ref(false)

// The create dialog is URL-driven (?create=1) so deep links, the header
// button, and history navigation all agree on whether it is open.
const createOpen = computed(() => canWrite.value && route.query.create === '1')
const createSiteId = ref('')
const hostname = ref('')
const kind = ref<Exclude<DomainKind, 'primary'>>('subdomain')
const redirectTarget = ref('')

const hostnameError = computed(() => {
  const value = hostname.value.trim().toLowerCase()
  if (!value) return undefined
  return domains.value.some((domain) => domain.hostname.toLowerCase() === value)
    ? 'This hostname is already in use on this node.'
    : undefined
})

// exactOptionalPropertyTypes: optional props with possibly-undefined values
// are passed as conditional v-bind objects instead of direct bindings.
const failureLink = computed(() =>
  runner.progress.value?.state === 'failed' && runner.jobId.value !== undefined
    ? { jobId: runner.jobId.value }
    : {},
)

const storedFailureLink = computed(() =>
  selected.value?.lastJobId !== undefined ? { jobId: selected.value.lastJobId } : {},
)

const progressExtras = computed(() => ({
  messages: runner.messages.value,
  ...(runner.startedAtMs.value !== undefined ? { startedAtMs: runner.startedAtMs.value } : {}),
}))

const hostnameFieldError = computed(() => (hostnameError.value ? { error: hostnameError.value } : {}))

function failureHint(failure?: string) {
  return failure ? { description: failure } : {}
}

function select(domain: Domain) {
  if (selected.value?.id !== domain.id) {
    runner.error.value = ''
    planError.value = ''
    removeOpen.value = false
  }
  selected.value = domain
  if (route.query.selected !== domain.id) {
    void router.replace({ query: { ...route.query, selected: domain.id } })
  }
}

// Restore ?selected= once the list first arrives.
let selectionRestored = typeof route.query.selected !== 'string'
watch(
  domains,
  (list) => {
    if (selectionRestored || list.length === 0) return
    selectionRestored = true
    const found = list.find((domain) => domain.id === route.query.selected)
    if (found) selected.value = found
  },
  { immediate: true },
)

watch(
  () => selected.value?.id,
  async (id, previousId) => {
    if (!id || id === previousId) return
    await nextTick()
    detailEl.value?.focus({ preventScroll: true })
    detailEl.value?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
  },
)

onMounted(() => {
  if (typeof route.query.site === 'string') {
    createSiteId.value = route.query.site
    siteFilter.value = route.query.site
  }
})

function openCreate() {
	if (!canWrite.value) return
  if (!createOpen.value) void router.replace({ query: { ...route.query, create: '1' } })
}

function closeCreate() {
  if (!createOpen.value) return
  const query = { ...route.query }
  delete query.create
  void router.replace({ query })
}

async function refreshSelected(domainId: string) {
  await domainsQuery.refetch()
  const found = domains.value.find((domain) => domain.id === domainId)
  if (found) select(found)
}

async function openPlan(domain: Domain) {
  planLoading.value = true
  planError.value = ''
  try {
    const response = await getDomainPlan(domain.id)
    plan.value = response.plan
    planExpiresAt.value = response.expiresAt
    planOpen.value = true
  } catch (caught) {
    planError.value = caught instanceof Error ? caught.message : 'The routing plan could not be loaded.'
  } finally {
    planLoading.value = false
  }
}

async function submitCreate() {
	if (!canWrite.value) return
  if (hostnameError.value || !createSiteId.value) return
  let createdId = ''
  await runner.run(
    async () => {
      const result = await createDomain({
        siteId: createSiteId.value,
        hostname: hostname.value.trim().toLowerCase(),
        kind: kind.value,
        ...(kind.value === 'redirect' ? { redirectTarget: redirectTarget.value.trim() } : {}),
      })
      createdId = result.domain.id
      return result.job.id
    },
    {
      onSettled: () => refreshSelected(createdId),
      onSuccess: async () => {
        closeCreate()
        hostname.value = ''
        redirectTarget.value = ''
        if (selected.value) await openPlan(selected.value)
      },
      failureMessage: 'The domain could not be added.',
    },
  )
}

async function approvePlan() {
  if (!identity.can('operations.apply')) return
  const domain = selected.value
  if (!domain) return
  planOpen.value = false
  await runner.run(async () => (await activateDomain(domain.id)).id, {
    onSettled: () => refreshSelected(domain.id),
    failureMessage: 'The domain could not be activated.',
    successToast: 'Domain activated',
  })
}

async function removeDomain() {
  const domain = selected.value
  if (!domain || !canApply.value) return
  removeOpen.value = false
  // A 409 guard (domain_primary_protected / domain_busy) rejects the DELETE
  // before a job is queued; runner.run then surfaces the server's message.
  await runner.run(async () => (await deleteDomain(domain.id)).id, {
    onSettled: () => {
      void domainsQuery.refetch()
    },
    onSuccess: () => {
      selected.value = undefined
      const query = { ...route.query }
      delete query.selected
      void router.replace({ query })
    },
    failureMessage: 'The domain could not be removed.',
    successToast: `${domain.hostname} was removed`,
  })
}

function kindLabel(domain: Domain): string {
  return domain.kind === 'redirect' && domain.redirectTarget
    ? `redirect → ${domain.redirectTarget}`
    : domain.kind
}

const detailFacts = computed<Fact[]>(() => {
  const domain = selected.value
  if (!domain) return []
  const site = sites.value.find((item) => item.id === domain.siteId)
  return [
    { label: 'Hostname', value: domain.hostname, mono: true },
    { label: 'Site', value: site?.displayName ?? domain.siteId, to: `/sites/${domain.siteId}` },
    { label: 'Kind', value: kindLabel(domain) },
    {
      label: 'Resolved addresses',
      value: domain.resolvedAddresses.join(', ') || 'No records',
      mono: domain.resolvedAddresses.length > 0,
    },
    { label: 'Added', value: formatDateTime(domain.createdAt) },
  ]
})

const planTitle = computed(() => `Activate ${selected.value?.hostname ?? 'domain'}`)

const planFacts = computed<Fact[]>(() => {
  const current = plan.value
  const domain = selected.value
  if (!current || !domain) return []
  const site = current.nodePlan.site
  return [
    { label: 'Hostname', value: domain.hostname, mono: true },
    { label: 'Site', value: site.primaryDomain, to: `/sites/${site.id}` },
    { label: 'Kind', value: kindLabel(domain) },
    { label: 'Hostnames after change', value: String(site.routes?.length ?? 0) },
  ]
})

const planSteps = computed(() => {
  const current = plan.value
  if (!current) return []
  return [
    ...current.nodePlan.artifacts.map((artifact) => `Write ${artifact.path}`),
    'Validate the full Nginx configuration and reload',
  ]
})

const planWarnings = computed(() =>
  plan.value ? [...new Set([...plan.value.warnings, ...plan.value.nodePlan.warnings])] : [],
)

const planDnsRows = computed(() =>
  plan.value && selected.value
    ? [{ hostname: selected.value.hostname, addresses: plan.value.resolvedAddresses }]
    : [],
)
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Routing"
      title="Domains"
      description="Point extra hostnames at your sites. Every change is validated as one atomic Nginx update before it goes live."
    >
      <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">Add domain</AppButton>
    </PageHeader>

    <AppCard flush>
      <div class="space-y-3 p-3 sm:p-4">
        <ListToolbar v-model:search="search" :count="matching" count-label="domains" placeholder="Search hostnames">
          <template #filters>
            <div class="w-44">
              <Combobox v-model="siteFilterModel">
                <ComboboxAnchor as-child>
                  <ComboboxTrigger
                    aria-label="Filter by site"
                    :label="(
                      (id) => (id === ALL_SITES ? 'All sites' : (sites.find((site) => site.id === id)?.displayName ?? ''))
                    )(siteFilterModel)"
                  />
                </ComboboxAnchor>
                <ComboboxList>
                  <ComboboxInput placeholder="Search sites…" />
                  <ComboboxEmpty>No sites match.</ComboboxEmpty>
                  <ComboboxGroup>
                    <ComboboxItem :value="ALL_SITES" text-value="All sites">All sites<ComboboxItemIndicator /></ComboboxItem>
                    <ComboboxItem v-for="site in sites" :key="site.id" :value="site.id" :text-value="site.displayName">
                      {{ site.displayName }}<ComboboxItemIndicator />
                    </ComboboxItem>
                  </ComboboxGroup>
                </ComboboxList>
              </Combobox>
            </div>
          </template>
        </ListToolbar>

        <div v-if="domainsQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="n in 3" :key="n" />
        </div>
        <AppAlert v-else-if="domainsQuery.isError.value" tone="danger">
          <p>Domains could not be loaded.</p>
          <AppButton size="sm" class="mt-2" @click="domainsQuery.refetch()">Retry</AppButton>
        </AppAlert>
        <EmptyState
          v-else-if="domains.length === 0"
          icon="globe"
          title="No domains yet"
          description="Point a subdomain, alias, or redirect at one of your sites."
        >
          <template #action>
            <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">Add domain</AppButton>
          </template>
        </EmptyState>
        <EmptyState
          v-else-if="matching === 0"
          icon="search"
          title="No matching domains"
          description="Try a different search or site filter."
        />
        <template v-else>
          <div class="space-y-1">
            <ResourceRow
              v-for="domain in pageItems"
              :key="domain.id"
              :title="domain.hostname"
              :subtitle="kindLabel(domain)"
              icon="globe"
              clickable
              :selected="domain.id === selected?.id"
              @select="select(domain)"
            >
              <template #meta>
                <span class="hidden shrink-0 font-mono text-xs text-ink-muted sm:inline">
                  {{ domain.resolvedAddresses[0] ?? 'DNS pending' }}
                </span>
              </template>
              <template #status>
                <StatusPill :status="domain.status" v-bind="failureHint(domain.failure)" />
              </template>
            </ResourceRow>
          </div>
          <div v-if="pageCount > 1" class="flex items-center justify-end gap-2 border-t border-outline pt-3">
            <AppButton size="sm" icon="chevron-left" aria-label="Previous page" :disabled="page <= 1" @click="page--" />
            <span class="text-xs text-ink-muted tabular-nums">Page {{ page }} of {{ pageCount }}</span>
            <AppButton size="sm" icon="chevron-right" aria-label="Next page" :disabled="page >= pageCount" @click="page++" />
          </div>
        </template>
      </div>
    </AppCard>

    <div
      v-if="selected"
      ref="detailEl"
      tabindex="-1"
      class="scroll-mt-6 rounded-2xl outline-none focus-visible:ring-2 focus-visible:ring-accent-400/40"
    >
      <AppCard eyebrow="Domain" :title="selected.hostname">
        <template #actions>
          <StatusPill :status="selected.status" v-bind="failureHint(selected.failure)" />
        </template>

        <div class="space-y-4">
          <JobFailureNotice v-if="selected.failure" :message="selected.failure" v-bind="storedFailureLink" />
          <JobFailureNotice
            v-if="runner.error.value && !createOpen"
            :message="runner.error.value"
            v-bind="failureLink"
          />
          <AppAlert v-if="planError" tone="danger">{{ planError }}</AppAlert>
          <JobProgress
            v-if="runner.busy.value && runner.progress.value && !createOpen"
            :event="runner.progress.value"
            v-bind="progressExtras"
          />

          <FactList :facts="detailFacts" />

          <div v-if="selected.status === 'plan_ready'" class="flex flex-wrap gap-2">
            <AppButton
              variant="primary"
              :loading="planLoading"
              :disabled="runner.busy.value"
              @click="openPlan(selected)"
            >
              Review plan
            </AppButton>
          </div>
          <div
            v-else-if="selected.status === 'active'"
            class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-accent-400/25 bg-accent-400/[0.06] px-4 py-3"
          >
            <p class="text-[13px] text-ink-secondary">{{ selected.hostname }} is live. Serve it over HTTPS next.</p>
            <AppButton v-if="identity.can('certificates.write')" variant="primary" size="sm" @click="router.push(`/certificates?site=${selected.siteId}&create=1`)">
              Enable HTTPS →
            </AppButton>
          </div>

          <!-- The primary domain is removed by deleting its site, not on its own. -->
          <div
            v-if="canApply && selected.kind !== 'primary'"
            class="flex flex-wrap items-center justify-between gap-3 border-t border-outline pt-4"
          >
            <div class="min-w-0">
              <p class="text-[13px] font-medium text-ink">Remove this domain</p>
              <p class="text-xs text-ink-muted">
                Its routing is stripped from the site's live Nginx configuration, then the record is deleted.
              </p>
            </div>
            <AppButton variant="danger" icon="trash" size="sm" :disabled="runner.busy.value" @click="removeOpen = true">
              Remove domain
            </AppButton>
          </div>
        </div>
      </AppCard>
    </div>

    <AppDialog :open="createOpen" title="Add domain" @close="closeCreate">
      <form class="space-y-4" @submit.prevent="submitCreate">
        <FormField label="Site">
          <div class="w-full">
            <Combobox v-model="createSiteId">
              <ComboboxAnchor as-child>
                <ComboboxTrigger
                  aria-label="Site"
                  placeholder="Select site"
                  :label="((id) => sites.find((site) => site.id === id)?.displayName ?? '')(createSiteId)"
                />
              </ComboboxAnchor>
              <ComboboxList>
                <ComboboxInput placeholder="Search sites…" />
                <ComboboxEmpty>No sites match.</ComboboxEmpty>
                <ComboboxGroup>
                  <ComboboxItem
                    v-for="site in sites"
                    :key="site.id"
                    :value="site.id"
                    :text-value="site.displayName"
                  >
                    {{ site.displayName }}<ComboboxItemIndicator />
                  </ComboboxItem>
                </ComboboxGroup>
              </ComboboxList>
            </Combobox>
          </div>
        </FormField>
        <FormField label="Kind" hint="An alias serves the site directly; a redirect sends visitors to another hostname.">
          <Select v-model="kind">
            <SelectTrigger />
            <SelectContent>
              <SelectItem value="subdomain">Subdomain</SelectItem>
              <SelectItem value="alias">Alias</SelectItem>
              <SelectItem value="redirect">Redirect</SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="Hostname" v-bind="hostnameFieldError">
          <!-- Displayed lowercase; the value is normalized on submit so typing keeps the caret. -->
          <AppInput v-model="hostname" class="lowercase" placeholder="www.example.com" :invalid="Boolean(hostnameError)" required />
        </FormField>
        <FormField v-if="kind === 'redirect'" label="Redirect target">
          <AppInput v-model="redirectTarget" placeholder="example.com" required />
        </FormField>

        <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="failureLink" />
        <JobProgress
          v-if="runner.busy.value && runner.progress.value"
          :event="runner.progress.value"
          v-bind="progressExtras"
        />

        <AppButton
          variant="primary"
          type="submit"
          :loading="runner.busy.value"
          :disabled="Boolean(hostnameError)"
          class="w-full"
        >
          Add domain
        </AppButton>
      </form>
    </AppDialog>

    <PlanReviewDialog
      :open="planOpen"
      :title="planTitle"
      :steps="planSteps"
      :facts="planFacts"
      :warnings="planWarnings"
      :expires-at="planExpiresAt"
      :busy="planLoading || runner.busy.value"
      :can-approve="identity.can('operations.apply')"
      approve-label="Activate domain"
      :can-regenerate="false"
      @approve="approvePlan"
      @close="planOpen = false"
    >
      <div>
        <p class="mb-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">DNS preflight</p>
        <ul class="space-y-1.5">
          <li
            v-for="row in planDnsRows"
            :key="row.hostname"
            class="rounded-lg border border-outline bg-canvas/40 px-3 py-2"
          >
            <div class="flex flex-wrap items-center justify-between gap-2">
              <span class="min-w-0 font-mono text-xs break-all text-ink">{{ row.hostname }}</span>
              <StatusPill
                v-if="row.addresses.length"
                tone="info"
                label="resolves"
                description="DNS records exist for this hostname. This does not confirm they point at this server."
              />
              <StatusPill v-else tone="warning" label="no records" />
            </div>
            <p v-if="row.addresses.length" class="mt-1 font-mono text-xs break-all text-ink-muted">
              {{ row.addresses.join(', ') }}
            </p>
            <p v-else class="mt-1 text-xs text-amber-300">
              Add an A record for {{ row.hostname }} pointing at this server before continuing.
            </p>
          </li>
        </ul>
      </div>
    </PlanReviewDialog>

    <AppConfirmDialog
      v-if="selected"
      :open="canApply && removeOpen"
      :title="`Remove ${selected.hostname}`"
      confirm-label="Remove domain"
      :busy="runner.busy.value"
      @confirm="removeDomain"
      @close="removeOpen = false"
    >
      This removes <strong class="font-semibold text-ink">{{ selected.hostname }}</strong> from the site's live Nginx
      configuration and deletes the record. Visitors using this hostname will no longer reach the site.
    </AppConfirmDialog>
  </section>
</template>
