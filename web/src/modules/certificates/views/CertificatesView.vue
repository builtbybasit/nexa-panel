<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { listSites } from '@/modules/sites/api'
import { useIdentityStore } from '@/modules/identity/store'
import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { daysUntil, formatDateTime } from '@/shared/formatters'
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
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'
import {
  Combobox,
  ComboboxEmpty,
  ComboboxFloatingContent,
  ComboboxSelectItem,
  ComboboxTriggerInput,
} from '@/shared/ui/combobox'

import {
  applyCertificate,
  createCertificate,
  getCertificatePlan,
  listCertificates,
  prepareCertificate,
  type Certificate,
  type CertificateOperation,
  type CertificatePlan,
} from '../api'

const ACME_EMAIL_KEY = 'nexa.acme-email'

const operationVerbs: Record<CertificateOperation, string> = { issue: 'Issue', renew: 'Renew', revoke: 'Revoke' }
const operationToasts: Record<CertificateOperation, string> = {
  issue: 'Certificate issued',
  renew: 'Certificate renewed',
  revoke: 'Certificate revoked',
}

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('certificates.write'))
const runner = useJobRunner()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites })
const certificatesQuery = useQuery({ queryKey: ['certificates'], queryFn: () => listCertificates() })
const sites = computed(() => sitesQuery.data.value ?? [])
const certificates = computed(() => certificatesQuery.data.value ?? [])

function expirySortKey(certificate: Certificate): number {
  return certificate.expiresAt ? Date.parse(certificate.expiresAt) : Number.MAX_SAFE_INTEGER
}

// Soonest expiry first; certificates without one (not yet issued) sort last.
const sortedCertificates = computed(() => [...certificates.value].sort((a, b) => expirySortKey(a) - expirySortKey(b)))

const { search, page, pageCount, items: pageItems, matching } = useCollection(() => sortedCertificates.value, {
  searchText: (certificate) => `${certificate.primaryDomain} ${certificate.domains.join(' ')} ${certificate.email}`,
})

const selected = ref<Certificate>()
const detailEl = ref<HTMLElement>()

const plan = ref<CertificatePlan>()
const planExpiresAt = ref('')
const planOpen = ref(false)
const planLoading = ref(false)
const planError = ref('')

// The create dialog is URL-driven (?create=1) so deep links, the header
// button, and history navigation all agree on whether it is open.
const createOpen = computed(() => canWrite.value && route.query.create === '1')
const createSiteId = ref('')
const email = ref(localStorage.getItem(ACME_EMAIL_KEY) ?? '')

const revokeOpen = ref(false)

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

function failureHint(failure?: string) {
  return failure ? { description: failure } : {}
}

function select(certificate: Certificate) {
  if (selected.value?.id !== certificate.id) {
    runner.error.value = ''
    planError.value = ''
  }
  selected.value = certificate
  if (route.query.selected !== certificate.id) {
    void router.replace({ query: { ...route.query, selected: certificate.id } })
  }
}

// Restore ?selected= once the list first arrives.
let selectionRestored = typeof route.query.selected !== 'string'
watch(
  certificates,
  (list) => {
    if (selectionRestored || list.length === 0) return
    selectionRestored = true
    const found = list.find((certificate) => certificate.id === route.query.selected)
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
  if (typeof route.query.site === 'string') createSiteId.value = route.query.site
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

async function refreshSelected(certificateId: string) {
  await certificatesQuery.refetch()
  const found = certificates.value.find((certificate) => certificate.id === certificateId)
  if (found) select(found)
}

async function openPlan(certificate: Certificate) {
  planLoading.value = true
  planError.value = ''
  try {
    const response = await getCertificatePlan(certificate.id)
    plan.value = response.plan
    planExpiresAt.value = response.expiresAt
    planOpen.value = true
  } catch (caught) {
    planError.value = caught instanceof Error ? caught.message : 'The certificate plan could not be loaded.'
  } finally {
    planLoading.value = false
  }
}

async function submitCreate() {
  if (!canWrite.value) return
  if (!createSiteId.value) return
  let createdId = ''
  await runner.run(
    async () => {
      const result = await createCertificate(createSiteId.value, email.value.trim())
      createdId = result.certificate.id
      localStorage.setItem(ACME_EMAIL_KEY, email.value.trim())
      return result.job.id
    },
    {
      onSettled: () => refreshSelected(createdId),
      onSuccess: async () => {
        closeCreate()
        if (selected.value) await openPlan(selected.value)
      },
      failureMessage: 'The certificate plan could not be prepared.',
    },
  )
}

async function prepareOperation(operation: CertificateOperation) {
  if (!canWrite.value) return
  const certificate = selected.value
  if (!certificate) return
  await runner.run(async () => (await prepareCertificate(certificate.id, operation)).id, {
    onSettled: () => refreshSelected(certificate.id),
    onSuccess: async () => {
      if (selected.value) await openPlan(selected.value)
    },
    failureMessage: 'The certificate plan could not be prepared.',
  })
}

async function regeneratePlan() {
  if (!canWrite.value) return
  const operation = plan.value?.operation
  if (operation) await prepareOperation(operation)
}

async function approvePlan() {
  if (!identity.can('operations.apply')) return
  const certificate = selected.value
  const operation = plan.value?.operation ?? 'issue'
  if (!certificate) return
  planOpen.value = false
  await runner.run(async () => (await applyCertificate(certificate.id)).id, {
    onSettled: () => refreshSelected(certificate.id),
    failureMessage: 'The certificate operation failed. The previous certificate stays in place.',
    successToast: operationToasts[operation],
  })
}

function confirmRevoke() {
  if (!canWrite.value) return
  revokeOpen.value = false
  void prepareOperation('revoke')
}

function relativeDays(days: number): string {
  if (days === 0) return 'today'
  const unit = Math.abs(days) === 1 ? 'day' : 'days'
  return days > 0 ? `in ${days} ${unit}` : `${-days} ${unit} ago`
}

function withRelative(timestamp?: string): string {
  if (!timestamp) return 'Not issued'
  const days = daysUntil(timestamp)
  const absolute = formatDateTime(timestamp)
  return days === undefined ? absolute : `${absolute} (${relativeDays(days)})`
}

function expiryLabel(certificate: Certificate): string {
  const days = daysUntil(certificate.expiresAt)
  if (days === undefined) return 'Not issued'
  if (days <= 0) return 'Expired'
  return `Expires in ${days}d`
}

function expiryClass(certificate: Certificate): string {
  const days = daysUntil(certificate.expiresAt)
  if (days === undefined) return 'text-ink-muted'
  if (days <= 0) return 'text-rose-300'
  if (days < 30) return 'text-amber-300'
  return 'text-ink-muted'
}

const expiryPill = computed(() => {
  const days = daysUntil(selected.value?.expiresAt)
  if (days === undefined) return undefined
  if (days <= 0) return { tone: 'danger' as const, label: `Expired ${relativeDays(days)}` }
  if (days < 30) return { tone: 'warning' as const, label: `Expires ${relativeDays(days)}` }
  return undefined
})

const detailFacts = computed<Fact[]>(() => {
  const certificate = selected.value
  if (!certificate) return []
  const site = sites.value.find((item) => item.id === certificate.siteId)
  const facts: Fact[] = [
    { label: 'Site', value: site?.displayName ?? certificate.siteId, to: `/sites/${certificate.siteId}` },
    { label: 'Issued', value: withRelative(certificate.issuedAt) },
    { label: 'Expires', value: withRelative(certificate.expiresAt) },
    { label: 'Names covered', value: certificate.domains.join(', ') || certificate.primaryDomain },
    { label: 'ACME email', value: certificate.email },
  ]
  if (certificate.certificatePath) {
    facts.push({ label: 'Certificate path', value: certificate.certificatePath, mono: true })
  }
  return facts
})

const planTitle = computed(() => {
  const certificate = selected.value
  const operation = plan.value?.operation
  if (!certificate || !operation) return 'Certificate plan'
  return `${operationVerbs[operation]} certificate for ${certificate.primaryDomain}`
})

const planApproveLabel = computed(() => `${operationVerbs[plan.value?.operation ?? 'issue']} certificate`)

const planFacts = computed<Fact[]>(() => {
  const current = plan.value
  if (!current) return []
  return [
    { label: 'Operation', value: current.operation },
    { label: 'Names covered', value: current.agentPlan.request.domains.join(', ') },
    { label: 'Certificate path', value: current.agentPlan.certificatePath, mono: true },
    { label: 'Private key path', value: current.agentPlan.privateKeyPath, mono: true },
  ]
})

const planDnsRows = computed(() =>
  plan.value ? Object.entries(plan.value.dns).map(([hostname, addresses]) => ({ hostname, addresses })) : [],
)
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="HTTPS"
      title="Certificates"
      description="Free Let's Encrypt certificates keep your sites on HTTPS. A failed renewal never removes a working certificate."
    >
      <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">Enable HTTPS</AppButton>
    </PageHeader>

    <AppCard flush>
      <div class="space-y-3 p-3 sm:p-4">
        <ListToolbar
          v-model:search="search"
          :count="matching"
          count-label="certificates"
          placeholder="Search domains or email"
        />

        <div v-if="certificatesQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="n in 3" :key="n" />
        </div>
        <AppAlert v-else-if="certificatesQuery.isError.value" tone="danger">
          <p>Certificates could not be loaded.</p>
          <AppButton size="sm" class="mt-2" @click="certificatesQuery.refetch()">Retry</AppButton>
        </AppAlert>
        <EmptyState
          v-else-if="certificates.length === 0"
          icon="shield"
          title="No certificates yet"
          description="Enable HTTPS to serve a managed site securely."
        >
          <template #action>
            <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">Enable HTTPS</AppButton>
          </template>
        </EmptyState>
        <EmptyState
          v-else-if="matching === 0"
          icon="search"
          title="No matching certificates"
          description="Try a different search."
        />
        <template v-else>
          <div class="space-y-1">
            <ResourceRow
              v-for="certificate in pageItems"
              :key="certificate.id"
              :title="certificate.primaryDomain"
              :subtitle="`${certificate.domains.length} names · ${certificate.email}`"
              icon="shield"
              clickable
              :selected="certificate.id === selected?.id"
              @select="select(certificate)"
            >
              <template #meta>
                <span class="shrink-0 font-mono text-xs" :class="expiryClass(certificate)">
                  {{ expiryLabel(certificate) }}
                </span>
              </template>
              <template #status>
                <StatusPill
                  v-if="certificate.expiringSoon && certificate.status === 'active'"
                  tone="warning"
                  label="expires soon"
                  description="Renew before the expiry date to avoid interruption."
                />
                <StatusPill v-else :status="certificate.status" v-bind="failureHint(certificate.failure)" />
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
      <AppCard eyebrow="Certificate" :title="selected.primaryDomain">
        <template #actions>
          <StatusPill v-if="expiryPill" :tone="expiryPill.tone" :label="expiryPill.label" :pulse="false" />
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

          <div class="flex flex-wrap gap-2">
            <AppButton
              v-if="selected.status === 'plan_ready'"
              variant="primary"
              :loading="planLoading"
              :disabled="runner.busy.value"
              @click="openPlan(selected)"
            >
              Review plan
            </AppButton>
            <template v-else-if="selected.status === 'active' && canWrite">
              <AppButton icon="refresh-cw" :disabled="runner.busy.value" @click="prepareOperation('renew')">
                Renew certificate
              </AppButton>
              <AppButton variant="danger" icon="x" :disabled="runner.busy.value" @click="revokeOpen = true">
                Revoke certificate
              </AppButton>
            </template>
            <AppButton
              v-else-if="selected.status === 'revoked' && canWrite"
              variant="primary"
              :disabled="runner.busy.value"
              @click="prepareOperation('issue')"
            >
              Reissue certificate
            </AppButton>
          </div>
        </div>
      </AppCard>
    </div>

    <AppDialog :open="createOpen" title="Enable HTTPS" @close="closeCreate">
      <form class="space-y-4" @submit.prevent="submitCreate">
        <FormField label="Site">
          <div class="w-full">
            <Combobox v-model="createSiteId">
              <ComboboxTriggerInput
                aria-label="Site"
                placeholder="Select site"
                :display-value="
                  (id) => {
                    const site = sites.find((item) => item.id === id)
                    return site ? `${site.displayName} · ${site.primaryDomain}` : ''
                  }
                "
              />
              <ComboboxFloatingContent>
                <ComboboxEmpty>No sites match.</ComboboxEmpty>
                <ComboboxSelectItem
                  v-for="site in sites"
                  :key="site.id"
                  :value="site.id"
                  :text-value="`${site.displayName} ${site.primaryDomain}`"
                >
                  {{ site.displayName }} · {{ site.primaryDomain }}
                </ComboboxSelectItem>
              </ComboboxFloatingContent>
            </Combobox>
          </div>
        </FormField>
        <FormField label="ACME contact email" hint="Let's Encrypt sends expiry notices to this address.">
          <AppInput v-model="email" type="email" placeholder="admin@example.com" required />
        </FormField>
        <p class="text-[13px] leading-relaxed text-ink-secondary">
          Every active hostname on the site is included after a DNS check. You review the plan before anything is
          issued.
        </p>

        <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="failureLink" />
        <JobProgress
          v-if="runner.busy.value && runner.progress.value"
          :event="runner.progress.value"
          v-bind="progressExtras"
        />

        <AppButton variant="primary" type="submit" :loading="runner.busy.value" class="w-full">Enable HTTPS</AppButton>
      </form>
    </AppDialog>

    <PlanReviewDialog
      :open="planOpen"
      :title="planTitle"
      :facts="planFacts"
      :expires-at="planExpiresAt"
      :busy="planLoading || runner.busy.value"
      :can-approve="identity.can('operations.apply')"
      :can-regenerate="canWrite"
      :approve-label="planApproveLabel"
      @approve="approvePlan"
      @regenerate="regeneratePlan"
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
      :open="canWrite && revokeOpen"
      title="Revoke certificate"
      confirm-label="Revoke certificate"
      @confirm="confirmRevoke"
      @close="revokeOpen = false"
    >
      {{ selected?.primaryDomain }} will stop serving HTTPS. Visitors get connection errors until a new certificate is
      issued.
    </AppConfirmDialog>
  </section>
</template>
