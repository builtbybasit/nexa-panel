<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import { listCertificates, type Certificate } from '@/modules/certificates/api'
import { listDomains, type Domain } from '@/modules/domains/api'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { daysUntil, formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppIcon,
  EmptyState,
  FactList,
  JobFailureNotice,
  JobProgress,
  MetricCard,
  PageHeader,
  ResourceRow,
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'

import { activateSite, getSitePlan, listSites, prepareSitePlan, rollbackSite, type SitePlan } from '../api'

const route = useRoute()
const runner = useJobRunner()

const siteId = computed(() => String(route.params.siteId ?? ''))

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const domainsQuery = useQuery({ queryKey: ['domains', siteId], queryFn: () => listDomains(siteId.value), retry: false })
const certificatesQuery = useQuery({
  queryKey: ['certificates', siteId],
  queryFn: () => listCertificates(siteId.value),
  retry: false,
})

const site = computed(() => sitesQuery.data.value?.find((candidate) => candidate.id === siteId.value))
const domains = computed(() => domainsQuery.data.value ?? [])
const certificates = computed(() => certificatesQuery.data.value ?? [])

const plan = ref<SitePlan>()
const planExpiresAt = ref('')
const planError = ref('')
const activated = ref(false)
const rollbackOpen = ref(false)
const arrived = ref(false)
const planSection = ref<HTMLElement>()

const linkButtonClass =
  'inline-flex h-9 items-center gap-1.5 rounded-lg border border-outline-strong bg-white/[0.03] px-3 text-[13px] font-medium text-ink transition-colors hover:bg-white/[0.07]'

const artifactLabels: Record<string, string> = {
  'site-root': 'Starter file',
  'php-fpm-pool': 'PHP-FPM pool',
  'nginx-site': 'Nginx site',
}

const facts = computed<Fact[]>(() => {
  const current = site.value
  if (!current) return []
  return [
    { label: 'Primary domain', value: current.primaryDomain, mono: true },
    { label: 'Runtime', value: `PHP ${current.phpVersion}` },
    { label: 'Unix owner', value: current.unixUser, mono: true },
    { label: 'Site root', value: current.rootPath, mono: true, to: `/files?site=${current.id}` },
    { label: 'FPM socket', value: current.socketPath, mono: true },
    { label: 'Logs', value: 'View site logs', to: `/logs?site=${current.id}` },
  ]
})

const shortcuts = computed(() => [
  {
    label: 'Files',
    value: 'Browse files',
    detail: 'Upload and manage everything under the site root',
    icon: 'folder',
    to: `/files?site=${siteId.value}`,
  },
  {
    label: 'Logs',
    value: 'View logs',
    detail: 'Follow the access and error logs',
    icon: 'file-text',
    to: `/logs?site=${siteId.value}`,
  },
  {
    label: 'Scheduled tasks',
    value: 'Manage tasks',
    detail: 'Commands that run on a schedule for this site',
    icon: 'clock',
    to: `/schedules?site=${siteId.value}`,
  },
])

const nextSteps = computed(() => [
  { label: 'Upload files', icon: 'upload', to: `/files?site=${siteId.value}` },
  { label: 'Add domain', icon: 'globe', to: `/domains?site=${siteId.value}&create=1` },
  { label: 'Enable HTTPS', icon: 'lock', to: `/certificates?site=${siteId.value}&create=1` },
  { label: 'View logs', icon: 'file-text', to: `/logs?site=${siteId.value}` },
])

const domainKindLabels: Record<Domain['kind'], string> = {
  primary: 'Primary domain',
  subdomain: 'Subdomain',
  alias: 'Alias',
  redirect: 'Redirect',
}

function domainSubtitle(domain: Domain): string {
  if (domain.kind === 'redirect' && domain.redirectTarget) return `Redirects to ${domain.redirectTarget}`
  return domainKindLabels[domain.kind]
}

// exactOptionalPropertyTypes: only bind optional props when a value exists.
const storedFailureLink = computed(() =>
  site.value?.lastJobId !== undefined ? { jobId: site.value.lastJobId } : {},
)
const runnerFailureLink = computed(() =>
  runner.progress.value?.state === 'failed' && runner.jobId.value !== undefined ? { jobId: runner.jobId.value } : {},
)
const progressExtras = computed(() => ({
  messages: runner.messages.value,
  ...(runner.startedAtMs.value !== undefined ? { startedAtMs: runner.startedAtMs.value } : {}),
}))

const expiryToneClasses = {
  default: 'text-ink-secondary',
  warning: 'text-amber-300',
  danger: 'text-rose-300',
} as const

function certificateExpiry(certificate: Certificate): { text: string; tone: keyof typeof expiryToneClasses } {
  if (!certificate.expiresAt) return { text: 'Not issued yet', tone: 'default' }
  const days = daysUntil(certificate.expiresAt) ?? 0
  const when = formatDateTime(certificate.expiresAt)
  if (days <= 0) return { text: `Expired ${when}`, tone: 'danger' }
  const text = `Expires ${when} — ${days} day${days === 1 ? '' : 's'} left`
  return { text, tone: certificate.expiringSoon || days <= 14 ? 'warning' : 'default' }
}

// --- Plan expiry countdown --------------------------------------------------

const now = ref(Date.now())
let ticker: ReturnType<typeof setInterval> | undefined

const expiresAtMs = computed(() => {
  if (!planExpiresAt.value) return undefined
  const parsed = Date.parse(planExpiresAt.value)
  return Number.isFinite(parsed) ? parsed : undefined
})
const remainingMs = computed(() => (expiresAtMs.value === undefined ? undefined : expiresAtMs.value - now.value))
const planExpired = computed(() => remainingMs.value !== undefined && remainingMs.value <= 0)
const countdown = computed(() => {
  const remaining = remainingMs.value
  if (remaining === undefined) return ''
  if (remaining <= 0) return 'Plan expired'
  const totalSeconds = Math.ceil(remaining / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  if (minutes >= 60) return `Expires in ${Math.floor(minutes / 60)}h ${minutes % 60}m`
  return `Expires in ${minutes}:${String(totalSeconds % 60).padStart(2, '0')}`
})
const countdownTone = computed(() => ((remainingMs.value ?? Infinity) < 60_000 ? 'danger' : 'warning'))

watch(
  expiresAtMs,
  (deadline) => {
    const shouldTick = deadline !== undefined
    if (shouldTick) now.value = Date.now()
    if (shouldTick && ticker === undefined) {
      ticker = setInterval(() => {
        now.value = Date.now()
      }, 1000)
    } else if (!shouldTick && ticker !== undefined) {
      clearInterval(ticker)
      ticker = undefined
    }
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (ticker !== undefined) clearInterval(ticker)
})

// --- Plan / activate / rollback --------------------------------------------

async function loadPlan() {
  const current = site.value
  if (!current || current.status !== 'plan_ready') return
  planError.value = ''
  try {
    const response = await getSitePlan(current.id)
    plan.value = response.plan
    planExpiresAt.value = response.expiresAt
  } catch (caught) {
    planError.value = caught instanceof Error ? caught.message : 'The plan could not be loaded.'
  }
}

async function focusPlanSection() {
  await nextTick()
  planSection.value?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  planSection.value?.focus({ preventScroll: true })
}

async function refreshSite() {
  await sitesQuery.refetch()
}

async function approve() {
  const current = site.value
  if (!current) return
  await runner.run(async () => (await activateSite(current.id)).id, {
    onSettled: refreshSite,
    onSuccess: () => {
      activated.value = true
      plan.value = undefined
      planExpiresAt.value = ''
    },
    failureMessage: 'Activation failed',
    successToast: `${current.primaryDomain} is live`,
  })
}

async function rollback() {
  const current = site.value
  if (!current) return
  rollbackOpen.value = false
  activated.value = false
  await runner.run(async () => (await rollbackSite(current.id)).id, {
    onSettled: refreshSite,
    failureMessage: 'Rollback failed',
    successToast: 'The previous configuration is live again',
  })
}

async function preparePlan() {
  const current = site.value
  if (!current) return
  plan.value = undefined
  planExpiresAt.value = ''
  await runner.run(async () => (await prepareSitePlan(current.id)).id, {
    onSettled: refreshSite,
    onSuccess: async () => {
      await loadPlan()
      await focusPlanSection()
    },
    failureMessage: 'Site planning failed',
  })
}

// On arrival: bring the plan into view, or pick up a job that is already running.
watch(
  site,
  async (current) => {
    if (!current || arrived.value) return
    arrived.value = true
    if (current.status === 'plan_ready') {
      await loadPlan()
      await focusPlanSection()
    } else if (['planning', 'activating', 'rolling_back'].includes(current.status) && current.lastJobId) {
      const wasActivating = current.status === 'activating'
      runner.follow(current.lastJobId, {
        onSettled: refreshSite,
        onSuccess: async () => {
          if (wasActivating) {
            activated.value = true
          } else if (site.value?.status === 'plan_ready') {
            await loadPlan()
            await focusPlanSection()
          }
        },
        failureMessage: wasActivating ? 'Activation failed' : 'The operation failed',
      })
    }
  },
  { immediate: true },
)

// Reset per-site state when navigating between site pages. reset() also
// clears busy, which stop() alone would leave latched forever.
watch(siteId, () => {
  arrived.value = false
  activated.value = false
  rollbackOpen.value = false
  plan.value = undefined
  planExpiresAt.value = ''
  planError.value = ''
  runner.reset()
})
</script>

<template>
  <section class="space-y-6">
    <template v-if="sitesQuery.isPending.value">
      <div class="space-y-1" aria-hidden="true">
        <SkeletonRow v-for="n in 3" :key="n" />
      </div>
    </template>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">
      <p>Sites could not be loaded.</p>
      <AppButton size="sm" class="mt-2" @click="sitesQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!site"
      icon="layers"
      title="Site not found"
      description="This site does not exist or may have been removed."
    >
      <template #action>
        <RouterLink to="/sites" :class="linkButtonClass">
          <AppIcon name="arrow-left" :size="14" />
          Back to sites
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else>
      <PageHeader
        :breadcrumbs="[{ label: 'Sites', to: '/sites' }, { label: site.displayName }]"
        :title="site.displayName"
        :description="site.primaryDomain"
      >
        <StatusPill :status="site.status" />
      </PageHeader>

      <div
        v-if="activated"
        class="rounded-2xl border border-emerald-400/25 bg-emerald-400/[0.06] p-5 sm:p-6"
        role="status"
      >
        <div class="flex items-center gap-2.5">
          <AppIcon name="check" :size="18" class="text-emerald-300" />
          <h3 class="text-[15px] font-semibold text-ink">{{ site.primaryDomain }} is live</h3>
        </div>
        <p class="mt-1.5 text-[13px] leading-relaxed text-ink-secondary">
          The site is serving visitors. Here is what you can do next.
        </p>
        <div class="mt-4 flex flex-wrap gap-2">
          <RouterLink v-for="step in nextSteps" :key="step.to" :to="step.to" :class="linkButtonClass">
            <AppIcon :name="step.icon" :size="14" />
            {{ step.label }}
          </RouterLink>
        </div>
      </div>

      <JobFailureNotice
        v-if="site.status === 'failed' && site.failure"
        :message="site.failure"
        v-bind="storedFailureLink"
      />
      <JobFailureNotice v-else-if="runner.error.value" :message="runner.error.value" v-bind="runnerFailureLink" />
      <JobProgress v-if="runner.progress.value" :event="runner.progress.value" v-bind="progressExtras" />

      <AppCard eyebrow="Overview">
        <FactList :facts="facts" />
      </AppCard>

      <section ref="planSection" tabindex="-1" class="scroll-mt-6 focus:outline-none" aria-label="Configuration plan">
        <!-- Plan ready: review and approve -->
        <AppCard
          v-if="site.status === 'plan_ready'"
          :eyebrow="`Configuration plan · ${site.slug}`"
          title="Review before activating"
        >
          <template #actions>
            <StatusPill v-if="countdown" :label="countdown" :tone="countdownTone" :pulse="false" class="normal-case" />
          </template>
          <div class="space-y-4">
            <AppAlert v-if="planError" tone="danger">
              <p>{{ planError }}</p>
              <AppButton size="sm" class="mt-2" @click="loadPlan">Retry</AppButton>
            </AppAlert>
            <div v-else-if="!plan" class="space-y-1" aria-hidden="true">
              <SkeletonRow v-for="n in 2" :key="n" />
            </div>
            <template v-else>
              <AppAlert v-if="planExpired" tone="danger">
                This plan has expired and can no longer be applied. Regenerate it to continue.
              </AppAlert>
              <AppAlert v-for="warning in plan.warnings" :key="warning" tone="warning">{{ warning }}</AppAlert>

              <div class="grid gap-3 sm:grid-cols-3">
                <article
                  v-for="artifact in plan.artifacts"
                  :key="artifact.path"
                  class="rounded-xl border border-outline bg-canvas/40 px-4 py-3"
                >
                  <span class="block text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">
                    {{ artifactLabels[artifact.kind] ?? artifact.kind }}
                  </span>
                  <code class="mt-1 block font-mono text-xs break-all text-accent-200">{{ artifact.path }}</code>
                  <small class="mt-1 block text-[11px] text-ink-muted">Mode {{ artifact.mode.toString(8) }}</small>
                </article>
              </div>

              <AppAlert tone="info">
                Activating checks the web server and PHP configuration, reloads services, and verifies the site responds.
                If anything fails, the previous state is restored automatically.
              </AppAlert>

              <div class="flex flex-wrap gap-2">
                <AppButton
                  v-if="planExpired"
                  variant="primary"
                  icon="refresh-cw"
                  :loading="runner.busy.value"
                  @click="preparePlan"
                >
                  Regenerate plan
                </AppButton>
                <AppButton v-else variant="primary" :loading="runner.busy.value" @click="approve">
                  Approve and activate
                </AppButton>
              </div>
            </template>
          </div>
        </AppCard>

        <!-- Active: offer rollback -->
        <AppCard
          v-else-if="site.status === 'active'"
          eyebrow="Configuration"
          title="This site is live"
          description="Roll back if something is wrong — visitors will see the previous configuration."
        >
          <AppButton variant="danger" icon="rotate-ccw" :disabled="runner.busy.value" @click="rollbackOpen = true">
            Roll back site
          </AppButton>
        </AppCard>

        <!-- A job is still running -->
        <AppCard
          v-else-if="['planning', 'activating', 'rolling_back'].includes(site.status)"
          eyebrow="Configuration"
          title="Update in progress"
        >
          <AppAlert v-if="!runner.progress.value" tone="info">
            This site is being updated right now.
            <RouterLink
              v-if="site.lastJobId"
              :to="`/jobs?job=${site.lastJobId}`"
              class="font-medium underline underline-offset-2"
            >
              View job #{{ site.lastJobId }}
            </RouterLink>
          </AppAlert>
          <p v-else class="text-[13px] text-ink-secondary">Progress is shown above.</p>
        </AppCard>

        <!-- Draft, rolled back, or failed: prepare a (new) plan -->
        <AppCard
          v-else
          eyebrow="Configuration"
          :title="site.status === 'rolled_back' ? 'Rolled back' : site.status === 'failed' ? 'The last change failed' : 'No plan yet'"
          :description="
            site.status === 'rolled_back'
              ? 'The previous configuration is live. Prepare a new plan to apply changes again.'
              : site.status === 'failed'
                ? 'Nothing was changed on the server. Prepare a new plan to try again.'
                : 'Prepare a plan to review exactly what will change before anything is applied.'
          "
        >
          <AppButton variant="primary" icon="refresh-cw" :loading="runner.busy.value" @click="preparePlan">
            {{ site.status === 'draft' ? 'Prepare plan' : 'Prepare new plan' }}
          </AppButton>
        </AppCard>
      </section>

      <div class="grid items-start gap-4 lg:grid-cols-2">
        <AppCard eyebrow="Routing" title="Domains">
          <template #actions>
            <RouterLink
              :to="`/domains?site=${site.id}&create=1`"
              class="inline-flex items-center gap-1 text-[13px] font-medium text-accent-300 transition-colors hover:text-accent-200"
            >
              <AppIcon name="plus" :size="14" />
              Add domain
            </RouterLink>
          </template>
          <div v-if="domainsQuery.isPending.value" class="space-y-1" aria-hidden="true">
            <SkeletonRow v-for="n in 2" :key="n" />
          </div>
          <AppAlert v-else-if="domainsQuery.isError.value" tone="danger">
            <p>Domains could not be loaded.</p>
            <AppButton size="sm" class="mt-2" @click="domainsQuery.refetch()">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="domains.length === 0"
            icon="globe"
            title="No domains yet"
            description="Add a domain to point more hostnames at this site."
          />
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="domain in domains"
              :key="domain.id"
              icon="globe"
              :title="domain.hostname"
              :subtitle="domainSubtitle(domain)"
            >
              <template #status>
                <StatusPill :status="domain.status" />
              </template>
            </ResourceRow>
          </div>
        </AppCard>

        <AppCard eyebrow="Security" title="HTTPS">
          <template #actions>
            <RouterLink
              :to="`/certificates?site=${site.id}&create=1`"
              class="inline-flex items-center gap-1 text-[13px] font-medium text-accent-300 transition-colors hover:text-accent-200"
            >
              <AppIcon name="lock" :size="14" />
              Enable HTTPS
            </RouterLink>
          </template>
          <div v-if="certificatesQuery.isPending.value" class="space-y-1" aria-hidden="true">
            <SkeletonRow v-for="n in 2" :key="n" />
          </div>
          <AppAlert v-else-if="certificatesQuery.isError.value" tone="danger">
            <p>Certificates could not be loaded.</p>
            <AppButton size="sm" class="mt-2" @click="certificatesQuery.refetch()">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="certificates.length === 0"
            icon="lock"
            title="HTTPS is not enabled"
            description="Issue a free certificate to serve this site over a secure connection."
          />
          <div v-else class="space-y-2">
            <div
              v-for="certificate in certificates"
              :key="certificate.id"
              class="rounded-xl border border-outline bg-canvas/40 px-4 py-3"
            >
              <div class="flex flex-wrap items-center justify-between gap-2">
                <span class="min-w-0 font-mono text-xs break-all text-accent-200">
                  {{ certificate.domains.join(', ') || certificate.primaryDomain }}
                </span>
                <StatusPill :status="certificate.status" />
              </div>
              <p class="mt-1.5 text-[13px]" :class="expiryToneClasses[certificateExpiry(certificate).tone]">
                {{ certificateExpiry(certificate).text }}
              </p>
            </div>
          </div>
        </AppCard>
      </div>

      <div class="grid gap-4 sm:grid-cols-3">
        <MetricCard
          v-for="shortcut in shortcuts"
          :key="shortcut.to"
          :label="shortcut.label"
          :value="shortcut.value"
          :detail="shortcut.detail"
          :icon="shortcut.icon"
          :to="shortcut.to"
        />
      </div>

      <AppConfirmDialog
        :open="rollbackOpen"
        :title="`Roll back ${site.primaryDomain}`"
        confirm-label="Roll back site"
        :busy="runner.busy.value"
        @confirm="rollback"
        @close="rollbackOpen = false"
      >
        Visitors will see the previous configuration as soon as the rollback finishes.
      </AppConfirmDialog>
    </template>
  </section>
</template>
