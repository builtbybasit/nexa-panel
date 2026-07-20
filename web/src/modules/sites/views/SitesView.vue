<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { useCollection } from '@/shared/composables/useCollection'
import { humanize } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppIcon,
  AppSelect,
  EmptyState,
  ListToolbar,
  PageHeader,
  ResourceRow,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

import { listSites, type SiteStatus } from '../api'

const router = useRouter()
const identity = useIdentityStore()
const canCreate = computed(() => identity.can('sites.write'))

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const sites = computed(() => sitesQuery.data.value ?? [])

const siteStatuses: SiteStatus[] = ['draft', 'planning', 'plan_ready', 'activating', 'active', 'rolling_back', 'rolled_back', 'failed']
const statusFilter = ref<SiteStatus | ''>('')

const filteredSites = computed(() =>
  statusFilter.value ? sites.value.filter((site) => site.status === statusFilter.value) : sites.value,
)

const { search, page, pageCount, items, matching } = useCollection(() => filteredSites.value, {
  searchText: (site) => `${site.displayName} ${site.primaryDomain} ${site.slug}`,
})

function openCreate() {
  if (!canCreate.value) return
  void router.push('/sites/new')
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Sites"
      description="Create a site and review the generated plan before an administrator activates it."
    >
      <AppButton v-if="canCreate" variant="primary" icon="plus" @click="openCreate">Create site</AppButton>
    </PageHeader>

    <AppCard flush>
      <div class="space-y-3 p-3 sm:p-4">
        <ListToolbar
          v-model:search="search"
          :count="matching"
          count-label="sites"
          placeholder="Search by name, domain, or slug"
        >
          <template #filters>
            <div class="w-44">
              <AppSelect v-model="statusFilter" aria-label="Filter by status">
                <option value="">All statuses</option>
                <option v-for="status in siteStatuses" :key="status" :value="status">{{ humanize(status) }}</option>
              </AppSelect>
            </div>
          </template>
        </ListToolbar>

        <div v-if="sitesQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="n in 3" :key="n" />
        </div>
        <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">
          <p>Sites could not be loaded.</p>
          <AppButton size="sm" class="mt-2" @click="sitesQuery.refetch()">Retry</AppButton>
        </AppAlert>
        <EmptyState
          v-else-if="sites.length === 0"
          icon="layers"
          title="No sites yet"
          description="Create your first site to get a plan you can review and activate."
        >
          <template #action>
            <AppButton v-if="canCreate" variant="primary" icon="plus" @click="openCreate">Create site</AppButton>
          </template>
        </EmptyState>
        <EmptyState
          v-else-if="items.length === 0"
          icon="search"
          title="No matching sites"
          description="Try a different search or status filter."
        />
        <template v-else>
          <div class="grid gap-2 sm:grid-cols-2">
            <!-- FastPanel-style dashed launcher sits alongside the site cards. -->
            <button
              v-if="canCreate"
              type="button"
              class="flex min-h-[68px] items-center justify-center gap-2 rounded-xl border border-dashed border-outline-strong text-[13px] font-medium text-ink-secondary transition-colors hover:border-accent-400/40 hover:bg-white/[0.03] hover:text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-400"
              @click="openCreate"
            >
              <AppIcon name="plus" :size="16" />
              Create site
            </button>
            <RouterLink
              v-for="site in items"
              :key="site.id"
              :to="`/sites/${site.id}`"
              class="block rounded-xl focus-visible:outline-2 focus-visible:outline-accent-400"
            >
              <ResourceRow
                :title="site.displayName"
                :subtitle="site.primaryDomain"
                :avatar="site.displayName.slice(0, 1).toUpperCase()"
                class="h-full hover:border-outline hover:bg-white/[0.03]"
              >
                <template #meta>
                  <span
                    v-if="site.status === 'failed' && site.failure"
                    class="hidden max-w-56 truncate text-xs text-rose-300 md:inline"
                    :title="site.failure"
                  >
                    {{ site.failure }}
                  </span>
                  <span class="hidden shrink-0 font-mono text-xs text-ink-muted sm:inline">PHP {{ site.phpVersion }}</span>
                </template>
                <template #status>
                  <StatusPill :status="site.status" />
                </template>
              </ResourceRow>
            </RouterLink>
          </div>
          <div v-if="pageCount > 1" class="flex items-center justify-end gap-2 pt-1 text-[13px] text-ink-muted">
            <AppButton size="sm" icon="chevron-left" aria-label="Previous page" :disabled="page <= 1" @click="page--" />
            <span class="tabular-nums">Page {{ page }} of {{ pageCount }}</span>
            <AppButton size="sm" icon="chevron-right" aria-label="Next page" :disabled="page >= pageCount" @click="page++" />
          </div>
        </template>
      </div>
    </AppCard>
  </section>
</template>
