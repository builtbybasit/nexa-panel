<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { listSites } from '@/modules/sites/api'
import { AppAlert, AppButton, EmptyState, PageHeader, SkeletonRow } from '@/shared/ui'
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

import SitePhpSettingsCard from '../components/SitePhpSettingsCard.vue'

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const activeSites = computed(() => (sitesQuery.data.value ?? []).filter((site) => site.status === 'active'))
const hasAnySites = computed(() => (sitesQuery.data.value ?? []).length > 0)

// The selected site lives in the ?site= query so a deep link (e.g. the tile on
// the site detail page) restores it and back/forward navigation works.
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
    <PageHeader eyebrow="Runtime configuration" title="Per-site PHP settings" description="Override php.ini directives for a single site. Overrides are written to the site's .user.ini and layer on top of its global PHP version settings.">
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
      icon="wrench"
      title="No site to configure"
      :description="
        hasAnySites
          ? 'Per-site PHP settings become available once a site reaches the active state.'
          : 'Every site runs on a PHP version whose settings you can override here. Create a site to get started.'
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
        <Combobox v-model="siteSelection">
          <ComboboxAnchor as-child>
            <ComboboxTrigger
              aria-label="Site"
              placeholder="Select a site"
              :label="((id) => {
                const site = activeSites.find((s) => s.id === id)
                return site ? `${site.displayName} — ${site.primaryDomain}` : ''
              })(siteSelection)"
            />
          </ComboboxAnchor>
          <ComboboxList>
            <ComboboxInput placeholder="Search sites…" />
            <ComboboxEmpty>No sites match.</ComboboxEmpty>
            <ComboboxGroup>
              <ComboboxItem
                v-for="site in activeSites"
                :key="site.id"
                :value="site.id"
                :text-value="`${site.displayName} ${site.primaryDomain}`"
              >
                {{ site.displayName }} — {{ site.primaryDomain }}
                <ComboboxItemIndicator />
              </ComboboxItem>
            </ComboboxGroup>
          </ComboboxList>
        </Combobox>
      </div>

      <SitePhpSettingsCard v-if="selectedSite" :key="selectedSite.id" :site-id="selectedSite.id" :php-version="selectedSite.phpVersion" />
    </template>
  </section>
</template>
