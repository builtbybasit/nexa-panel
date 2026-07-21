<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { listSites } from '@/modules/sites/api'
import { AppAlert, AppButton, AppSelect, EmptyState, PageHeader, SkeletonRow } from '@/shared/ui'

import SftpAccessCard from '../components/SftpAccessCard.vue'

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const activeSites = computed(() => (sitesQuery.data.value ?? []).filter((site) => site.status === 'active'))
const hasAnySites = computed(() => (sitesQuery.data.value ?? []).length > 0)

// The selected site lives in the ?site= query so the tile on the site detail
// page deep-links here and back/forward navigation restores the selection.
const siteId = computed(() => (typeof route.query.site === 'string' ? route.query.site : ''))
const selectedSite = computed(() => activeSites.value.find((site) => site.id === siteId.value) ?? activeSites.value[0])

const siteSelection = computed({
  get: () => selectedSite.value?.id ?? '',
  set: (id: string) => {
    const query = { ...route.query }
    if (id) query.site = id
    else delete query.site
    void router.replace({ query })
  },
})
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Access"
      title="SFTP access"
      description="Give a site its own SSH-jailed SFTP login. Access runs over the site's own system account with a chrooted home directory and no shell."
    >
      <RouterLink
        v-if="selectedSite"
        :to="`/sites/${selectedSite.id}`"
        class="text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
      >
        ← Back to site
      </RouterLink>
    </PageHeader>

    <div v-if="sitesQuery.isPending.value" class="space-y-1">
      <SkeletonRow v-for="n in 3" :key="n" />
    </div>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">
      <p>The site list failed to load.</p>
      <AppButton size="sm" class="mt-2" @click="sitesQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!activeSites.length"
      icon="server"
      title="No site to configure"
      :description="
        hasAnySites
          ? 'SFTP access becomes available once a site reaches the active state.'
          : 'SFTP access is granted per site. Create a site to get started.'
      "
    >
      <template #action>
        <RouterLink
          v-if="!hasAnySites && identity.can('sites.write')"
          to="/sites?create=1"
          class="text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
        >
          Create a site →
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else>
      <div class="w-full sm:w-80">
        <AppSelect v-model="siteSelection" aria-label="Site">
          <option v-for="site in activeSites" :key="site.id" :value="site.id">
            {{ site.displayName }} — {{ site.primaryDomain }}
          </option>
        </AppSelect>
      </div>

      <SftpAccessCard v-if="selectedSite" :key="selectedSite.id" :site-id="selectedSite.id" />
    </template>
  </section>
</template>
