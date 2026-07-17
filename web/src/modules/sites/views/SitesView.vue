<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, ref, watch, type ComponentPublicInstance } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { humanize } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppDialog,
  AppInput,
  AppSelect,
  EmptyState,
  FormField,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  ResourceRow,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

import { createSite, listRuntimes, listSites, type Site, type SiteStatus } from '../api'

const route = useRoute()
const router = useRouter()
const runner = useJobRunner()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const runtimesQuery = useQuery({ queryKey: ['runtimes'], queryFn: listRuntimes, retry: false })
const sites = computed(() => sitesQuery.data.value ?? [])
const runtimes = computed(() => runtimesQuery.data.value ?? [])

const siteStatuses: SiteStatus[] = ['draft', 'planning', 'plan_ready', 'activating', 'active', 'rolling_back', 'rolled_back', 'failed']
const statusFilter = ref<SiteStatus | ''>('')

const filteredSites = computed(() =>
  statusFilter.value ? sites.value.filter((site) => site.status === statusFilter.value) : sites.value,
)

const { search, page, pageCount, items, matching } = useCollection(() => filteredSites.value, {
  searchText: (site) => `${site.displayName} ${site.primaryDomain} ${site.slug}`,
})

// --- Create dialog ---------------------------------------------------------

const createOpen = computed(() => route.query.create === '1')

const displayName = ref('')
const slug = ref('')
const slugLocked = ref(true)
const primaryDomain = ref('')
const phpVersion = ref('')
const slugError = ref('')
const domainError = ref('')
const slugField = ref<ComponentPublicInstance>()
const createdSite = ref<Site>()

const SLUG_PATTERN = /^[a-z][a-z0-9-]{1,31}$/

function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 32)
}

watch(displayName, (name) => {
  if (slugLocked.value) slug.value = slugify(name)
})
watch(slug, () => {
  slugError.value = ''
})
watch(primaryDomain, () => {
  domainError.value = ''
})
// A fresh open never shows a previous attempt's failed progress or values.
watch(createOpen, (open) => {
  if (open && !runner.busy.value) {
    runner.error.value = ''
    runner.progress.value = undefined
    resetForm()
  }
})

function unlockSlug() {
  slugLocked.value = false
  void nextTick(() => (slugField.value?.$el as HTMLInputElement | undefined)?.focus())
}

function openCreate() {
  void router.replace({ query: { ...route.query, create: '1' } })
}

function closeCreate() {
  const { create: _create, ...rest } = route.query
  void router.replace({ query: rest })
}

function resetForm() {
  displayName.value = ''
  slug.value = ''
  slugLocked.value = true
  primaryDomain.value = ''
  phpVersion.value = ''
  slugError.value = ''
  domainError.value = ''
}

function validate(): boolean {
  const candidateSlug = slug.value.trim()
  const candidateDomain = primaryDomain.value.trim().toLowerCase()
  slugError.value = ''
  domainError.value = ''
  if (!SLUG_PATTERN.test(candidateSlug)) {
    slugError.value = 'Use 2–32 lowercase letters, digits, or hyphens, starting with a letter.'
  } else if (sites.value.some((site) => site.slug === candidateSlug)) {
    slugError.value = 'Another site already uses this slug. Choose a different one.'
  }
  if (sites.value.some((site) => site.primaryDomain.toLowerCase() === candidateDomain)) {
    domainError.value = 'Another site already uses this domain.'
  }
  return !slugError.value && !domainError.value
}

// exactOptionalPropertyTypes: only bind optional props when a value exists.
const failureLink = computed(() =>
  runner.progress.value?.state === 'failed' && runner.jobId.value !== undefined ? { jobId: runner.jobId.value } : {},
)
const progressExtras = computed(() => ({
  messages: runner.messages.value,
  ...(runner.startedAtMs.value !== undefined ? { startedAtMs: runner.startedAtMs.value } : {}),
}))

async function submit() {
  if (runner.busy.value || !validate()) return
  await runner.run(
    async () => {
      const result = await createSite({
        displayName: displayName.value.trim(),
        slug: slug.value.trim(),
        primaryDomain: primaryDomain.value.trim(),
        phpVersion: phpVersion.value,
      })
      createdSite.value = result.site
      await sitesQuery.refetch()
      return result.job.id
    },
    {
      onSettled: async () => {
        await sitesQuery.refetch()
      },
      onSuccess: () => {
        const created = createdSite.value
        resetForm()
        if (created) void router.push(`/sites/${created.id}`)
      },
      failureMessage: 'Site planning failed',
    },
  )
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Sites"
      description="Create a site, review the generated plan, then activate it. Nothing changes on the server until you approve."
    >
      <AppButton variant="primary" icon="plus" @click="openCreate">Create site</AppButton>
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
            <AppButton variant="primary" icon="plus" @click="openCreate">Create site</AppButton>
          </template>
        </EmptyState>
        <EmptyState
          v-else-if="items.length === 0"
          icon="search"
          title="No matching sites"
          description="Try a different search or status filter."
        />
        <template v-else>
          <div class="space-y-1">
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
                class="hover:border-outline hover:bg-white/[0.03]"
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

    <AppDialog :open="createOpen" title="Create site" @close="closeCreate">
      <form id="create-site-form" class="space-y-4" @submit.prevent="submit">
        <AppAlert v-if="runtimesQuery.isSuccess.value && runtimes.length === 0" tone="warning">
          No installed PHP runtime was found under <code class="font-mono">/etc/php</code>. Install and enable one before
          creating a site.
        </AppAlert>
        <AppAlert v-if="runtimesQuery.isError.value" tone="danger">
          PHP runtimes could not be loaded.
          <AppButton size="sm" class="mt-2" @click="runtimesQuery.refetch()">Retry</AppButton>
        </AppAlert>

        <FormField label="Display name">
          <AppInput v-model="displayName" maxlength="80" autocomplete="off" placeholder="Customer portal" required />
        </FormField>
        <FormField
          label="Slug"
          :error="slugError"
          :hint="
            slugLocked
              ? 'Created from the display name. Names the Unix user and folders; cannot be changed later.'
              : 'Lowercase letters, digits, and hyphens. Cannot be changed later.'
          "
        >
          <div class="flex gap-2">
            <AppInput
              ref="slugField"
              v-model="slug"
              maxlength="32"
              autocomplete="off"
              spellcheck="false"
              placeholder="customer-portal"
              class="font-mono"
              :readonly="slugLocked"
              :invalid="Boolean(slugError)"
              required
            />
            <AppButton v-if="slugLocked" icon="pencil" aria-label="Edit slug" @click="unlockSlug">Edit</AppButton>
          </div>
        </FormField>
        <FormField label="Primary domain" :error="domainError">
          <AppInput
            v-model="primaryDomain"
            maxlength="253"
            autocomplete="url"
            placeholder="portal.example.com"
            :invalid="Boolean(domainError)"
            required
          />
        </FormField>
        <FormField label="PHP version">
          <AppSelect v-model="phpVersion" empty-message="No PHP runtime found" required>
            <option disabled value="">Select a PHP version</option>
            <option v-for="runtime in runtimes" :key="runtime.version" :value="runtime.version">
              PHP {{ runtime.version }}{{ runtime.supportStatus === 'end_of_life_allowed' ? ' — no longer supported' : '' }}
            </option>
          </AppSelect>
        </FormField>

        <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="failureLink" />
        <JobProgress v-if="runner.progress.value" :event="runner.progress.value" v-bind="progressExtras" />
      </form>
      <template #footer>
        <AppButton :disabled="runner.busy.value" @click="closeCreate">Cancel</AppButton>
        <AppButton
          form="create-site-form"
          type="submit"
          variant="primary"
          :loading="runner.busy.value"
          :disabled="runtimes.length === 0"
        >
          Create site
        </AppButton>
      </template>
    </AppDialog>
  </section>
</template>
