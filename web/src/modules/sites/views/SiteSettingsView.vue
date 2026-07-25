<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import type { Job } from '@/modules/jobs/api'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  EmptyState,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  SkeletonRow,
} from '@/shared/ui'

import { listRuntimes, listSites, mergeSiteSettings, updateSiteSettings, type Site, type SiteSettings } from '../api'
import SiteSettingsCard from '../components/SiteSettingsCard.vue'

const route = useRoute()
const runner = useJobRunner()
const identity = useIdentityStore()

const canWriteSites = computed(() => identity.can('sites.write'))
const siteId = computed(() => String(route.params.siteId ?? ''))

// Shares the ['sites'] cache with the detail and list views, so saving here and
// navigating back shows the updated site without a second fetch.
const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const site = computed(() => sitesQuery.data.value?.find((candidate) => candidate.id === siteId.value))
// Same key as the create wizard, so the installed-runtime list is shared.
const runtimesQuery = useQuery({ queryKey: ['runtimes'], queryFn: listRuntimes, retry: false })
const runtimes = computed(() => runtimesQuery.data.value ?? [])

// A 202 returns a Job (numeric id + state) to follow; a 200 returns the updated
// Site (settled, non-active states persist only).
function isJob(response: Job | Site): response is Job {
  return typeof (response as Job).id === 'number' && 'state' in response
}

async function refreshSite() {
  await sitesQuery.refetch()
}

async function saveSettings(next: SiteSettings, phpVersion?: string) {
  const current = site.value
  if (!current || !canWriteSites.value) return
  await runner.run(
    async () => {
      const response = await updateSiteSettings(current.id, next, phpVersion)
      if (isJob(response)) return response.id
      // Persist-only: no job to follow, so refresh the site ourselves.
      await refreshSite()
      return undefined
    },
    {
      onSettled: refreshSite,
      successToast: phpVersion ? `Settings saved — the site now targets PHP ${phpVersion}` : 'Site settings saved',
      failureMessage: 'Saving site settings failed',
    },
  )
}

</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Site configuration"
      :title="site ? `Settings — ${site.primaryDomain}` : 'Site settings'"
      description="Tune the Nginx virtual host and PHP-FPM pool for this site. Changes are re-rendered and applied to the node; an active site is reloaded automatically."
    >
      <RouterLink
        v-if="site"
        :to="`/sites/${site.id}`"
        class="text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
      >
        ← Back to site
      </RouterLink>
    </PageHeader>

    <div v-if="sitesQuery.isPending.value" class="space-y-1">
      <SkeletonRow v-for="n in 3" :key="n" />
    </div>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">
      <p>The site could not be loaded.</p>
      <AppButton size="sm" class="mt-2" @click="sitesQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!site"
      icon="settings-2"
      title="Site not found"
      description="This site does not exist, or you do not have access to it."
    >
      <template #action>
        <RouterLink
          to="/sites"
          class="text-[13px] font-medium text-accent-300 underline-offset-2 hover:text-accent-200 hover:underline"
        >
          Back to sites →
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else>
      <JobFailureNotice v-if="runner.error.value" v-bind="runner.failureProps.value" />
      <JobProgress v-if="runner.progress.value" :event="runner.progress.value" v-bind="runner.progressProps.value" />

      <SiteSettingsCard
        :settings="mergeSiteSettings(site.settings)"
        :can-write="canWriteSites"
        :busy="runner.busy.value"
        :php-version="site.phpVersion"
        :runtimes="runtimes"
        @save="saveSettings"
      />
    </template>
  </section>
</template>
