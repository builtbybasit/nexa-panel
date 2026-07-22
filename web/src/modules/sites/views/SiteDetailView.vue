<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { listCertificates, type Certificate } from '@/modules/certificates/api'
import { listDomains, type Domain } from '@/modules/domains/api'
import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { daysUntil, formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppIcon,
  EmptyState,
  FeatureTile,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  ResourceRow,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

import {
  activateSite,
  deleteSite,
  getSitePlan,
  listSites,
  prepareSitePlan,
  rollbackSite,
  type Site,
  type SitePlan,
} from '../api'

const route = useRoute()
const router = useRouter()
const runner = useJobRunner()
const identity = useIdentityStore()

const canWriteSites = computed(() => identity.can('sites.write'))
const canApplyOperations = computed(() => identity.can('operations.apply'))
const canWriteDomains = computed(() => identity.can('domains.write'))
const canWriteCertificates = computed(() => identity.can('certificates.write'))
const canWriteFiles = computed(() => identity.can('files.write'))
const canReadDomains = computed(() => identity.can('domains.read'))
const canReadCertificates = computed(() => identity.can('certificates.read'))
const canReadFiles = computed(() => identity.can('files.read'))

const siteId = computed(() => String(route.params.siteId ?? ''))

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const domainsQuery = useQuery({
  queryKey: ['domains', siteId],
  queryFn: () => listDomains(siteId.value),
  enabled: canReadDomains,
  retry: false,
})
const certificatesQuery = useQuery({
  queryKey: ['certificates', siteId],
  queryFn: () => listCertificates(siteId.value),
  enabled: canReadCertificates,
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
const deleteOpen = ref(false)
const arrived = ref(false)
const planSection = ref<HTMLElement>()

const linkButtonClass =
  'inline-flex h-9 items-center gap-1.5 rounded-lg border border-outline-strong bg-white/[0.03] px-3 text-[13px] font-medium text-ink transition-colors hover:bg-white/[0.07]'

const artifactLabels: Record<string, string> = {
  'site-root': 'Starter file',
  'php-fpm-pool': 'PHP-FPM pool',
  'nginx-site': 'Nginx site',
}

const createdAtText = computed(() => (site.value ? formatDateTime(site.value.createdAt) : ''))

interface HeroFact {
  icon: string
  label: string
  value: string
  mono?: boolean
  to?: string
}

const heroFacts = computed<HeroFact[]>(() => {
  const current = site.value
  if (!current) return []
  return [
    { icon: 'user', label: 'Site user', value: current.unixUser, mono: true },
    {
      icon: 'folder',
      label: 'Root directory',
      value: current.rootPath,
      mono: true,
      ...(canReadFiles.value ? { to: `/files?site=${current.id}` } : {}),
    },
    { icon: 'plug', label: 'FPM socket', value: current.socketPath, mono: true },
    { icon: 'globe', label: 'Primary domain', value: current.primaryDomain, mono: true },
    { icon: 'file-code-2', label: 'Runtime', value: `PHP ${current.phpVersion} · PHP-FPM + Nginx` },
    { icon: 'clock', label: 'Created', value: createdAtText.value },
  ]
})

interface ManageTile {
  label: string
  icon: string
  to: string
}

// Only render workflows that exist today. Ids resolve through siteId so each
// launcher opens the current site's corresponding module.
const tiles = computed<ManageTile[]>(() => {
  const id = siteId.value
  const items: ManageTile[] = []
  if (identity.can('files.read')) items.push({ label: 'Files', icon: 'folder', to: `/files?site=${id}` })
  if (identity.can('databases.read')) items.push({ label: 'Databases', icon: 'database', to: '/databases' })
  if (identity.can('domains.read')) items.push({ label: 'Domains (DNS)', icon: 'globe', to: `/domains?site=${id}` })
  if (identity.can('certificates.read')) {
    items.push({ label: 'SSL certificates', icon: 'lock', to: `/certificates?site=${id}` })
  }
  if (identity.can('schedules.read')) items.push({ label: 'Scheduler', icon: 'clock', to: `/schedules?site=${id}` })
  if (identity.can('applications.read')) items.push({ label: 'PHP settings', icon: 'wrench', to: `/php/site?site=${id}` })
  if (identity.can('operations.apply')) items.push({ label: 'SFTP access', icon: 'server', to: `/sftp?site=${id}` })
  if (identity.can('logs.read')) items.push({ label: 'Logs', icon: 'file-text', to: `/logs?site=${id}` })
  if (identity.can('backups.read')) items.push({ label: 'Backup copies', icon: 'archive', to: '/backups' })
  items.push({ label: 'Settings', icon: 'settings-2', to: `/sites/${id}/settings` })
  return items
})

const nextSteps = computed(() => {
  const items: ManageTile[] = []
  if (identity.can('logs.read')) items.push({ label: 'View logs', icon: 'file-text', to: `/logs?site=${siteId.value}` })
  if (canWriteFiles.value) items.unshift({ label: 'Upload files', icon: 'upload', to: `/files?site=${siteId.value}` })
  if (canWriteDomains.value) items.push({ label: 'Add domain', icon: 'globe', to: `/domains?site=${siteId.value}&create=1` })
  if (canWriteCertificates.value) {
    items.push({ label: 'Enable HTTPS', icon: 'lock', to: `/certificates?site=${siteId.value}&create=1` })
  }
  return items
})

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
  if (!current || !canApplyOperations.value) return
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
  if (!current || !canApplyOperations.value) return
  rollbackOpen.value = false
  activated.value = false
  await runner.run(async () => (await rollbackSite(current.id)).id, {
    onSettled: refreshSite,
    failureMessage: 'Rollback failed',
    successToast: 'The previous configuration is live again',
  })
}

async function deleteSiteAction() {
  const current = site.value
  if (!current || !canApplyOperations.value) return
  deleteOpen.value = false
  // A 409 guard (site_busy / site_has_dependents) rejects the DELETE before a
  // job is queued; runner.run then surfaces the server's message (which lists
  // the blocking domains or the certificate to remove first) as runner.error.
  await runner.run(async () => (await deleteSite(current.id)).id, {
    onSettled: refreshSite,
    onSuccess: () => {
      void router.push('/sites')
    },
    failureMessage: 'Deleting the site failed',
    successToast: `${current.primaryDomain} was deleted`,
  })
}

async function preparePlan() {
  const current = site.value
  if (!current || !canWriteSites.value) return
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
    } else if (['planning', 'activating', 'rolling_back', 'deleting'].includes(current.status) && current.lastJobId) {
      const wasActivating = current.status === 'activating'
      const wasDeleting = current.status === 'deleting'
      runner.follow(current.lastJobId, {
        onSettled: refreshSite,
        onSuccess: async () => {
          if (wasDeleting) {
            void router.push('/sites')
          } else if (wasActivating) {
            activated.value = true
          } else if (site.value?.status === 'plan_ready') {
            await loadPlan()
            await focusPlanSection()
          }
        },
        failureMessage: wasDeleting ? 'Deleting the site failed' : wasActivating ? 'Activation failed' : 'The operation failed',
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
  deleteOpen.value = false
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

      <div class="rounded-2xl border border-outline bg-surface/80 p-5 sm:p-6">
        <div class="flex items-center gap-3.5">
          <div
            class="grid size-12 shrink-0 place-items-center rounded-xl border border-accent-400/20 bg-gradient-to-br from-accent-500/25 to-accent-500/[0.06] text-lg font-bold text-accent-200"
          >
            {{ site.displayName.charAt(0).toUpperCase() || '·' }}
          </div>
          <div class="min-w-0">
            <p class="truncate text-[15px] font-semibold text-ink">{{ site.displayName }}</p>
            <p class="truncate font-mono text-xs text-accent-200">{{ site.primaryDomain }}</p>
          </div>
        </div>
        <dl class="mt-5 grid gap-x-6 gap-y-4 border-t border-outline pt-5 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="fact in heroFacts" :key="fact.label" class="flex min-w-0 items-start gap-2.5">
            <span
              class="mt-px grid size-7 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.02] text-ink-muted"
            >
              <AppIcon :name="fact.icon" :size="14" />
            </span>
            <div class="min-w-0">
              <dt class="text-[10px] font-bold tracking-[0.1em] text-ink-muted uppercase">{{ fact.label }}</dt>
              <dd class="mt-0.5 truncate" :class="fact.mono ? 'font-mono text-xs text-accent-200' : 'text-[13px] text-ink'" :title="fact.value">
                <RouterLink
                  v-if="fact.to"
                  :to="fact.to"
                  class="underline-offset-2 transition-colors hover:text-accent-100 hover:underline"
                >
                  {{ fact.value }}
                </RouterLink>
                <template v-else>{{ fact.value }}</template>
              </dd>
            </div>
          </div>
        </dl>
      </div>

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
              <AppAlert v-if="!canApplyOperations" tone="info">
                You can review this plan, but only an administrator can activate it.
              </AppAlert>

              <div class="flex flex-wrap gap-2">
                <AppButton
                  v-if="planExpired && canWriteSites"
                  variant="primary"
                  icon="refresh-cw"
                  :loading="runner.busy.value"
                  @click="preparePlan"
                >
                  Regenerate plan
                </AppButton>
                <AppButton
                  v-else-if="!planExpired && canApplyOperations"
                  variant="primary"
                  :loading="runner.busy.value"
                  @click="approve"
                >
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
          :description="
            canApplyOperations
              ? 'Roll back if something is wrong — visitors will see the previous configuration.'
              : 'The active configuration is available for review. An administrator is required to roll it back.'
          "
        >
          <AppButton
            v-if="canApplyOperations"
            variant="danger"
            icon="rotate-ccw"
            :disabled="runner.busy.value"
            @click="rollbackOpen = true"
          >
            Roll back site
          </AppButton>
        </AppCard>

        <!-- A job is still running -->
        <AppCard
          v-else-if="['planning', 'activating', 'rolling_back', 'deleting'].includes(site.status)"
          eyebrow="Configuration"
          :title="site.status === 'deleting' ? 'Deletion in progress' : 'Update in progress'"
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
          <AppButton
            v-if="canWriteSites"
            variant="primary"
            icon="refresh-cw"
            :loading="runner.busy.value"
            @click="preparePlan"
          >
            {{ site.status === 'draft' ? 'Prepare plan' : 'Prepare new plan' }}
          </AppButton>
          <AppAlert v-else tone="info">Your account has read-only access to this site.</AppAlert>
        </AppCard>
      </section>

      <AppCard v-if="tiles.length" eyebrow="Manage" title="Site managing">
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          <FeatureTile v-for="tile in tiles" :key="tile.label" v-bind="tile" />
        </div>
      </AppCard>


      <div class="grid items-start gap-4 lg:grid-cols-2">
        <AppCard v-if="canReadDomains" eyebrow="Routing" title="Domains">
          <template #actions>
            <RouterLink
              v-if="canWriteDomains"
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

        <AppCard v-if="canReadCertificates" eyebrow="Security" title="HTTPS">
          <template #actions>
            <RouterLink
              v-if="canWriteCertificates"
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

      <AppCard
        v-if="canApplyOperations && site.status !== 'deleting'"
        eyebrow="Danger zone"
        title="Delete this site"
        description="Removes the site's Nginx and PHP-FPM configuration from the server and deletes it from the panel. This cannot be undone."
      >
        <AppAlert tone="warning" class="mb-4">
          Detach any extra domains and remove its TLS certificate first — deletion is blocked while either still points at this
          site so the server is never left with orphaned configuration.
        </AppAlert>
        <AppButton
          variant="danger"
          icon="trash"
          :disabled="runner.busy.value"
          @click="deleteOpen = true"
        >
          Delete site
        </AppButton>
      </AppCard>

      <AppConfirmDialog
        :open="canApplyOperations && rollbackOpen"
        :title="`Roll back ${site.primaryDomain}`"
        confirm-label="Roll back site"
        :busy="runner.busy.value"
        @confirm="rollback"
        @close="rollbackOpen = false"
      >
        Visitors will see the previous configuration as soon as the rollback finishes.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="canApplyOperations && deleteOpen"
        :title="`Delete ${site.primaryDomain}`"
        confirm-label="Delete site"
        :type-to-confirm="site.primaryDomain"
        :busy="runner.busy.value"
        @confirm="deleteSiteAction"
        @close="deleteOpen = false"
      >
        This permanently removes <strong class="font-semibold text-ink">{{ site.displayName }}</strong> and strips its
        configuration from the server. Visitors will no longer be served. This cannot be undone.
      </AppConfirmDialog>
    </template>
  </section>
</template>
