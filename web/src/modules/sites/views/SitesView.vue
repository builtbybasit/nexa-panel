<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { formatTime } from '@/shared/formatters'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppInput,
  AppSelect,
  EmptyState,
  FactList,
  FormField,
  JobProgress,
  PageHeader,
  ResourceRow,
  StatusPill,
} from '@/shared/ui'

import { activateSite, createSite, getSitePlan, listRuntimes, listSites, prepareSitePlan, rollbackSite, type Site, type SitePlan } from '../api'

const displayName = ref('')
const slug = ref('')
const primaryDomain = ref('')
const phpVersion = ref('')

const selectedSite = ref<Site>()
const selectedPlan = ref<SitePlan>()
const planExpiresAt = ref('')

const runner = useJobRunner()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const runtimesQuery = useQuery({ queryKey: ['runtimes'], queryFn: listRuntimes, retry: false })
const sites = computed(() => sitesQuery.data.value ?? [])
const runtimes = computed(() => runtimesQuery.data.value ?? [])

const artifactLabels: Record<string, string> = {
  'site-root': 'Starter file',
  'php-fpm-pool': 'PHP-FPM pool',
  'nginx-site': 'Nginx site',
}

const siteFacts = computed(() =>
  selectedSite.value
    ? [
        { label: 'Unix owner', value: selectedSite.value.unixUser, mono: true },
        { label: 'Site root', value: selectedSite.value.rootPath, mono: true },
        { label: 'FPM socket', value: selectedSite.value.socketPath, mono: true },
        { label: 'Runtime', value: `PHP ${selectedSite.value.phpVersion}` },
      ]
    : [],
)

async function refreshSelected(siteId: string) {
  await sitesQuery.refetch()
  selectedSite.value = sites.value.find((site) => site.id === siteId) ?? selectedSite.value
}

async function submit() {
  selectedPlan.value = undefined
  await runner.run(
    async () => {
      const result = await createSite({
        displayName: displayName.value,
        slug: slug.value,
        primaryDomain: primaryDomain.value,
        phpVersion: phpVersion.value,
      })
      selectedSite.value = result.site
      await sitesQuery.refetch()
      return result.job.id
    },
    {
      onSettled: async () => {
        if (selectedSite.value) await refreshSelected(selectedSite.value.id)
      },
      onSuccess: async () => {
        if (selectedSite.value) await loadPlan(selectedSite.value)
      },
      failureMessage: 'Planning failed. Open Jobs for the durable failure record.',
    },
  )
}

async function loadPlan(site: Site) {
  selectedSite.value = site
  selectedPlan.value = undefined
  planExpiresAt.value = ''
  runner.error.value = ''
  if (!site.lastJobId && site.status !== 'plan_ready') return
  try {
    const response = await getSitePlan(site.id)
    selectedPlan.value = response.plan
    planExpiresAt.value = response.expiresAt
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The configuration plan could not be loaded.'
  }
}

async function mutateSite(rollback: boolean) {
  const site = selectedSite.value
  if (!site) return
  await runner.run(async () => (rollback ? await rollbackSite(site.id) : await activateSite(site.id)).id, {
    onSettled: () => refreshSelected(site.id),
    failureMessage: 'The node operation failed and restoration was attempted. Inspect Jobs for details.',
  })
}

async function refreshPlan() {
  const site = selectedSite.value
  if (!site) return
  await runner.run(async () => (await prepareSitePlan(site.id)).id, {
    onSettled: () => refreshSelected(site.id),
    onSuccess: async () => {
      if (selectedSite.value) await loadPlan(selectedSite.value)
    },
    failureMessage: 'Site planning failed. Open Jobs for the durable failure record.',
  })
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Managed web hosting"
      title="Sites"
      description="Every site change is planned first, validated on the node, and activated atomically with automatic restore on failure."
    >
      <StatusPill tone="accent" label="Plan-first workflow" :pulse="false" />
    </PageHeader>

    <AppAlert v-if="runtimesQuery.isSuccess.value && runtimes.length === 0" tone="warning">
      No installed PHP-FPM 7.4 or 8.x runtime was discovered under <code class="font-mono">/etc/php</code>. Install and enable
      one before creating a site.
    </AppAlert>
    <AppAlert v-if="sitesQuery.isError.value || runtimesQuery.isError.value" tone="danger">
      Site or runtime inventory is unavailable. Check the API and node configuration.
    </AppAlert>

    <div class="grid gap-4 lg:grid-cols-[minmax(0,380px)_1fr]">
      <AppCard
        eyebrow="Desired state"
        title="Create a PHP site"
        description="Nexa derives the Unix owner, document root, socket, and config paths from the immutable slug."
      >
        <form class="space-y-4" @submit.prevent="submit">
          <FormField label="Display name">
            <AppInput v-model="displayName" maxlength="80" autocomplete="off" placeholder="Customer portal" required />
          </FormField>
          <FormField label="Immutable slug" hint="Lowercase letters, digits, and hyphens. Cannot be changed later.">
            <AppInput v-model="slug" minlength="2" maxlength="32" pattern="[a-z][a-z0-9-]+" autocomplete="off" placeholder="customer-portal" required />
          </FormField>
          <FormField label="Primary domain">
            <AppInput v-model="primaryDomain" maxlength="253" autocomplete="url" placeholder="portal.example.com" required />
          </FormField>
          <FormField label="Installed PHP runtime">
            <AppSelect v-model="phpVersion" required>
              <option disabled value="">Select a discovered runtime</option>
              <option v-for="runtime in runtimes" :key="runtime.version" :value="runtime.version">
                PHP {{ runtime.version }}{{ runtime.supportStatus === 'end_of_life_allowed' ? ' — legacy / end of life' : '' }}
              </option>
            </AppSelect>
          </FormField>

          <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
          <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />

          <AppButton variant="primary" type="submit" :loading="runner.busy.value" :disabled="runtimes.length === 0" class="w-full">
            Create and prepare plan
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="Desired resources" :title="`${sites.length} managed ${sites.length === 1 ? 'site' : 'sites'}`" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="sites.length" class="space-y-1">
            <ResourceRow
              v-for="site in sites"
              :key="site.id"
              :title="site.displayName"
              :subtitle="site.primaryDomain"
              :avatar="site.displayName.slice(0, 1).toUpperCase()"
              clickable
              @select="loadPlan(site)"
            >
              <template #meta>
                <span class="hidden shrink-0 font-mono text-xs text-ink-muted sm:inline">PHP {{ site.phpVersion }}</span>
              </template>
              <template #status>
                <StatusPill :status="site.status" />
              </template>
            </ResourceRow>
          </div>
          <EmptyState
            v-else
            icon="layers"
            title="No sites yet"
            description="Create the first desired site to generate its reviewed node plan."
            class="m-2"
          />
        </div>
      </AppCard>
    </div>

    <AppCard
      v-if="selectedSite"
      :eyebrow="`Configuration plan · ${selectedSite.slug}`"
      :title="selectedPlan ? 'Artifacts ready for node validation' : 'Plan is not ready'"
    >
      <template #actions>
        <StatusPill v-if="planExpiresAt" tone="warning" :label="`Expires ${formatTime(planExpiresAt)}`" :pulse="false" />
      </template>

      <div class="space-y-5">
        <FactList :facts="siteFacts" />

        <div v-if="selectedPlan" class="grid gap-3 sm:grid-cols-3">
          <article
            v-for="artifact in selectedPlan.artifacts"
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

        <AppAlert v-if="selectedPlan" tone="warning">
          Activation validates PHP-FPM and <code class="font-mono">nginx -t</code>, reloads services, verifies the Host
          response, and restores captured state on failure.
        </AppAlert>

        <div v-if="selectedPlan" class="flex flex-wrap gap-2">
          <AppButton
            v-if="selectedSite.status !== 'active'"
            icon="refresh-cw"
            :disabled="runner.busy.value"
            @click="refreshPlan"
          >
            Refresh signed plan
          </AppButton>
          <AppButton
            v-if="selectedSite.status !== 'active'"
            variant="primary"
            :loading="runner.busy.value"
            :disabled="selectedSite.status === 'activating'"
            @click="mutateSite(false)"
          >
            Approve and activate
          </AppButton>
          <AppButton v-else variant="danger" icon="rotate-ccw" :loading="runner.busy.value" @click="mutateSite(true)">
            Rollback activation
          </AppButton>
        </div>
      </div>
    </AppCard>
  </section>
</template>
