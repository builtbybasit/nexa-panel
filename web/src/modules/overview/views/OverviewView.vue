<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'
import { RouterLink } from 'vue-router'

import { listCertificates } from '@/modules/certificates/api'
import { listJobs } from '@/modules/jobs/api'
import { useIdentityStore } from '@/modules/identity/store'
import { listSites } from '@/modules/sites/api'
import { getSystemOverview } from '@/modules/system/api'
import { formatBytes, formatDateTime, formatJobKind } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppIcon,
  EmptyState,
  MetricCard,
  PageHeader,
  SkeletonCard,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

const identity = useIdentityStore()
const systemQuery = useQuery({ queryKey: ['system', 'overview'], queryFn: getSystemOverview, retry: false, refetchInterval: 15_000 })
const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const jobsQuery = useQuery({ queryKey: ['jobs'], queryFn: listJobs, retry: false, refetchInterval: 10_000 })
const certificatesQuery = useQuery({
  queryKey: ['certificates'],
  queryFn: () => listCertificates(),
  enabled: computed(() => identity.can('certificates.read')),
  retry: false,
})

const sites = computed(() => sitesQuery.data.value ?? [])
const jobs = computed(() => jobsQuery.data.value ?? [])
const certificates = computed(() => certificatesQuery.data.value ?? [])
const memory = computed(() => systemQuery.data.value?.memory)
const warnings = computed(() => systemQuery.data.value?.warnings ?? [])

const activeSites = computed(() => sites.value.filter((site) => site.status === 'active').length)
const runningJobs = computed(() => jobs.value.filter((job) => job.state === 'queued' || job.state === 'running'))
const recentJobs = computed(() => jobs.value.slice(0, 6))
const expiringCertificates = computed(() => certificates.value.filter((certificate) => certificate.expiringSoon).length)

const quickActions = computed(() => [
  { label: 'Create site', icon: 'layers', to: '/sites/new', permission: 'sites.write' as const },
  { label: 'Add domain', icon: 'globe', to: '/domains?create=1', permission: 'domains.write' as const },
  { label: 'Enable HTTPS', icon: 'lock', to: '/certificates?create=1', permission: 'certificates.write' as const },
  { label: 'New database', icon: 'database', to: '/databases?create=1', permission: 'databases.write' as const },
  { label: 'Schedule a task', icon: 'clock', to: '/schedules?create=1', permission: 'schedules.write' as const },
  { label: 'Browse files', icon: 'folder', to: '/files', permission: 'files.read' as const },
].filter((action) => identity.can(action.permission)))

</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Local node"
      title="Server overview"
      description="Observed state across sites, databases, certificates, and durable operations on this node."
    />

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <SkeletonCard v-if="sitesQuery.isPending.value" />
      <MetricCard
        v-else
        label="Managed sites"
        icon="layers"
        to="/sites"
        :value="sitesQuery.isError.value ? '—' : sites.length"
        :detail="sitesQuery.isError.value ? 'Inventory unavailable' : `${activeSites} active through Nginx`"
      />
      <SkeletonCard v-if="systemQuery.isPending.value" />
      <MetricCard
        v-else
        label="Memory available"
        icon="cpu"
        :value="memory ? formatBytes(memory.availableBytes) : '—'"
        :detail="memory ? `${Math.round(memory.usedPercent)}% used · ${memory.profile} profile` : 'Capacity not reported'"
      />
      <SkeletonCard v-if="jobsQuery.isPending.value" />
      <MetricCard
        v-else
        label="Active jobs"
        icon="history"
        to="/jobs"
        :value="jobsQuery.isError.value ? '—' : runningJobs.length"
        :detail="jobsQuery.isError.value ? 'Job history unavailable' : `${jobs.length} recorded operations`"
      />
      <SkeletonCard v-if="identity.can('certificates.read') && certificatesQuery.isPending.value" />
      <MetricCard
        v-else-if="identity.can('certificates.read')"
        label="TLS certificates"
        icon="shield"
        to="/certificates"
        :value="certificatesQuery.isError.value ? '—' : certificates.length"
        :tone="!certificatesQuery.isError.value && expiringCertificates ? 'warning' : 'default'"
        :detail="
          certificatesQuery.isError.value
            ? 'Certificate state unavailable'
            : expiringCertificates
              ? `${expiringCertificates} expiring within 30 days — review`
              : 'No upcoming expirations'
        "
      />
    </div>

    <div v-if="warnings.length" class="space-y-2">
      <p
        v-for="warning in warnings"
        :key="warning"
        class="rounded-xl border border-amber-400/25 bg-amber-400/[0.07] px-4 py-3 text-[13px] text-amber-200"
      >
        {{ warning }}
      </p>
    </div>

    <div class="grid gap-4 lg:grid-cols-[1.4fr_1fr]">
      <AppCard eyebrow="Durable operations" title="Recent jobs" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="jobsQuery.isPending.value" class="space-y-1 px-2 py-2" aria-hidden="true">
            <SkeletonRow v-for="n in 3" :key="n" />
          </div>
          <div v-else-if="jobsQuery.isError.value" class="m-2 flex flex-col items-start gap-3">
            <AppAlert tone="danger" class="w-full">Recent jobs couldn't be loaded.</AppAlert>
            <AppButton size="sm" icon="refresh-cw" @click="jobsQuery.refetch()">Retry</AppButton>
          </div>
          <div v-else-if="recentJobs.length" class="divide-y divide-outline">
            <RouterLink
              v-for="job in recentJobs"
              :key="job.id"
              :to="`/jobs?job=${job.id}`"
              class="flex items-center gap-3 px-2 py-3 transition-colors hover:bg-white/[0.02]"
            >
              <span class="font-mono text-xs text-ink-muted">#{{ job.id }}</span>
              <span class="min-w-0 flex-1">
                <strong class="block truncate text-[13px] font-semibold text-ink">{{ formatJobKind(job.kind) }}</strong>
                <small class="block truncate text-xs text-ink-muted">
                  {{ formatDateTime(job.createdAt) }}
                </small>
              </span>
              <StatusPill :status="job.state" />
            </RouterLink>
          </div>
          <EmptyState
            v-else
            icon="history"
            title="No operations yet"
            description="Every change on this node is executed as a durable, auditable job."
            class="m-2"
          />
        </div>
      </AppCard>

      <AppCard eyebrow="Shortcuts" title="Quick actions">
        <nav class="grid gap-1.5" aria-label="Quick actions">
          <RouterLink
            v-for="action in quickActions"
            :key="action.to"
            :to="action.to"
            class="group flex items-center gap-3 rounded-xl border border-transparent px-3 py-2.5 transition-colors hover:border-outline hover:bg-white/[0.03]"
          >
            <span class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-accent-300">
              <AppIcon :name="action.icon" :size="16" />
            </span>
            <strong class="min-w-0 flex-1 text-[13px] font-semibold text-ink">{{ action.label }}</strong>
            <AppIcon name="chevron-right" :size="14" class="shrink-0 text-ink-muted transition-transform group-hover:translate-x-0.5" />
          </RouterLink>
        </nav>
      </AppCard>
    </div>
  </section>
</template>
