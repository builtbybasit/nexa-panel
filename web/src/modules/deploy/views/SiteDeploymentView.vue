<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import { listSites } from '@/modules/sites/api'
import { AppAlert, AppButton, EmptyState, PageHeader, SkeletonRow } from '@/shared/ui'

import DeployKeyCard from '../components/DeployKeyCard.vue'
import DeploymentModeCard from '../components/DeploymentModeCard.vue'
import NodePrerequisitesCard from '../components/NodePrerequisitesCard.vue'
import SharedEnvCard from '../components/SharedEnvCard.vue'
import SshAccessCard from '../components/SshAccessCard.vue'

const route = useRoute()
const siteId = computed(() => String(route.params.siteId ?? ''))

// Shares the ['sites'] cache with the detail view, so arriving here from the
// site's tile grid resolves the site without a second fetch.
const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const site = computed(() => sitesQuery.data.value?.find((candidate) => candidate.id === siteId.value))
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Deployment"
      :title="site ? `Deployment — ${site.primaryDomain}` : 'Deployment'"
      description="Deployment access and layout for this site. SSH runs over the site's own system account, key-only, the deploy key reaches one GitHub repository, and the deployment mode decides whether the panel or a deploy tool owns the document root."
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
      icon="rocket"
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
      <SshAccessCard :key="`ssh-${site.id}`" :site-id="site.id" />
      <DeployKeyCard :key="`deploy-key-${site.id}`" :site-id="site.id" :primary-domain="site.primaryDomain" />
      <DeploymentModeCard
        :key="`mode-${site.id}`"
        :site-id="site.id"
        :primary-domain="site.primaryDomain"
        :root-path="site.rootPath"
        :mode="site.deploymentMode"
        @changed="sitesQuery.refetch()"
      />
      <!-- The shared file only exists once the release tree does, so the card
           appears with the mode rather than explaining an empty state. -->
      <SharedEnvCard v-if="site.deploymentMode === 'deployer'" :key="`env-${site.id}`" :site-id="site.id" />
      <!-- Last because it is the one card about the node rather than the site:
           the tooling it installs is shared by every site on the box. -->
      <NodePrerequisitesCard :key="`prereq-${site.id}`" :site-id="site.id" />
    </template>
  </section>
</template>
