<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'

import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  EmptyState,
  type Fact,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  PlanReviewDialog,
  SkeletonCard,
  StatusPill,
} from '@/shared/ui'

import { applyPlan, getPlan, listApplications, prepareChange, type Application, type ApplicationPlan, type AppAction } from '../api'

const router = useRouter()

const appsQuery = useQuery({ queryKey: ['applications'], queryFn: listApplications, retry: false })
const applications = computed(() => appsQuery.data.value ?? [])

const collection = useCollection<Application>(() => applications.value, {
  searchText: (app) => `${app.label} ${app.summary} ${app.app} ${app.version ?? ''}`,
  pageSize: 200,
})

// FastPanel-style catalog grouped into sections, in a stable presentation order.
const categories = [
  { key: 'php', label: 'PHP versions', icon: 'wrench' },
  { key: 'database', label: 'Databases', icon: 'database' },
  { key: 'runtime', label: 'Runtimes', icon: 'server' },
  { key: 'tooling', label: 'Developer tools', icon: 'archive' },
  { key: 'web-client', label: 'Database web clients', icon: 'terminal' },
]

const grouped = computed(() =>
  categories
    .map((category) => ({ ...category, items: collection.items.value.filter((app) => app.category === category.key) }))
    .filter((group) => group.items.length),
)

const runner = useJobRunner()
const applyRunner = useJobRunner()

const pendingId = ref<string>()
const selected = ref<Application>()
const selectedPlan = ref<ApplicationPlan>()
const planError = ref('')
const confirmApp = ref<Application>()

const anyBusy = computed(() => runner.busy.value || applyRunner.busy.value)

const reviewFacts = computed<Fact[]>(() => {
  const app = selected.value
  const plan = selectedPlan.value
  if (!app || !plan) return []
  return [
    { label: 'Application', value: app.label },
    { label: 'Action', value: plan.operation === 'package.remove' ? 'Uninstall' : 'Install' },
    { label: 'Packages', value: plan.agentPlan.packages.join(', '), mono: true },
  ]
})

const approveLabel = computed(() => (selectedPlan.value?.operation === 'package.remove' ? 'Uninstall' : 'Install'))

function canInstall(app: Application) {
  return app.managed && (app.status === 'available' || app.status === 'failed')
}
function canUninstall(app: Application) {
  return app.managed && app.status === 'installed'
}
function canReview(app: Application) {
  return app.managed && app.status === 'plan_ready'
}

async function loadPlan(app: Application) {
  selected.value = app
  planError.value = ''
  selectedPlan.value = undefined
  try {
    selectedPlan.value = (await getPlan(app.id)).plan
  } catch (caught) {
    planError.value = caught instanceof Error ? caught.message : 'The plan is not ready yet.'
  }
}

async function prepare(app: Application, action: AppAction) {
  pendingId.value = app.id
  await runner.run(async () => (await prepareChange(app.id, action)).job.id, {
    onSettled: async () => {
      await appsQuery.refetch()
    },
    onSuccess: async () => {
      await loadPlan(app)
    },
    failureMessage: `Preparing the ${app.label} change failed`,
  })
  pendingId.value = undefined
}

function startInstall(app: Application) {
  void prepare(app, 'package.install')
}

function requestUninstall(app: Application) {
  confirmApp.value = app
}

function confirmUninstall() {
  const app = confirmApp.value
  confirmApp.value = undefined
  if (app) void prepare(app, 'package.remove')
}

function closeReview() {
  selectedPlan.value = undefined
  selected.value = undefined
}

function regenerate() {
  const app = selected.value
  const operation = selectedPlan.value?.operation
  if (!app || !operation) return
  selectedPlan.value = undefined
  void prepare(app, operation)
}

async function approve() {
  const app = selected.value
  if (!app) return
  const removing = selectedPlan.value?.operation === 'package.remove'
  await applyRunner.run(async () => (await applyPlan(app.id)).id, {
    onSettled: async () => {
      selectedPlan.value = undefined
      selected.value = undefined
      await appsQuery.refetch()
    },
    successToast: `${app.label} ${removing ? 'removed' : 'installed'}`,
    failureMessage: `The ${app.label} operation failed`,
  })
}

function goManage(app: Application) {
  if (app.manageHref) void router.push(app.manageHref)
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Software catalog"
      title="Applications"
      description="Install and remove server software — PHP versions, PostgreSQL, Node.js, and Composer — through reviewed, agent-signed apt plans. Database web clients are deployed from the DB web clients page."
    >
      <StatusPill tone="accent" label="apt · reviewed installs" :pulse="false" />
    </PageHeader>

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
    <JobFailureNotice v-if="applyRunner.error.value" :message="applyRunner.error.value" :job-id="applyRunner.jobId.value" />
    <JobProgress v-if="runner.progress.value" :event="runner.progress.value" :messages="runner.messages.value" :started-at-ms="runner.startedAtMs.value" />
    <JobProgress v-if="applyRunner.progress.value" :event="applyRunner.progress.value" :messages="applyRunner.messages.value" :started-at-ms="applyRunner.startedAtMs.value" />
    <AppAlert v-if="planError" tone="danger">{{ planError }}</AppAlert>

    <ListToolbar
      v-if="!appsQuery.isPending.value && !appsQuery.isError.value"
      :search="collection.search.value"
      :count="collection.matching.value"
      count-label="applications"
      placeholder="Search applications"
      @update:search="collection.search.value = $event"
    />

    <div v-if="appsQuery.isPending.value" class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      <SkeletonCard v-for="n in 6" :key="n" />
    </div>
    <AppAlert v-else-if="appsQuery.isError.value" tone="danger">
      <p>The application catalog couldn't be loaded. The node agent may be unreachable.</p>
      <AppButton size="sm" class="mt-2" icon="refresh-cw" @click="appsQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!collection.matching.value"
      icon="download"
      title="No matching applications"
      description="No catalog entries match your search."
    />

    <div v-else class="space-y-8">
      <section v-for="group in grouped" :key="group.key" class="space-y-3">
        <h2 class="text-[11px] font-bold tracking-[0.12em] text-ink-muted uppercase">{{ group.label }}</h2>
        <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <AppCard v-for="app in group.items" :key="app.id" :title="app.label">
            <template #actions>
              <StatusPill :status="app.status" />
            </template>
            <div class="flex h-full flex-col gap-4">
              <p class="text-[13px] text-ink-secondary">{{ app.summary }}</p>
              <dl v-if="app.installedVersion" class="text-xs text-ink-muted">
                <dt class="inline font-semibold text-ink-secondary">Installed version:</dt>
                <dd class="ml-1 inline font-mono text-accent-200">{{ app.installedVersion }}</dd>
              </dl>

              <JobFailureNotice
                v-if="app.status === 'failed' && app.failure"
                :message="app.failure"
                :job-id="app.lastJobId"
              />

              <div class="mt-auto flex flex-wrap gap-2 pt-1">
                <AppButton
                  v-if="canInstall(app)"
                  variant="primary"
                  icon="download"
                  :loading="pendingId === app.id"
                  :disabled="anyBusy"
                  @click="startInstall(app)"
                >
                  Install
                </AppButton>
                <AppButton v-if="canReview(app)" icon="eye" :disabled="anyBusy" @click="loadPlan(app)">
                  Review plan
                </AppButton>
                <AppButton
                  v-if="canUninstall(app)"
                  variant="danger"
                  icon="trash"
                  :disabled="anyBusy"
                  @click="requestUninstall(app)"
                >
                  Uninstall
                </AppButton>
                <AppButton v-if="!app.managed" icon="external-link" @click="goManage(app)">
                  Manage
                </AppButton>
              </div>
            </div>
          </AppCard>
        </div>
      </section>
    </div>

    <PlanReviewDialog
      :open="!!selectedPlan"
      :title="selected ? `Review ${selected.label} plan` : 'Review plan'"
      :facts="reviewFacts"
      :steps="selectedPlan?.agentPlan.steps ?? []"
      :warnings="selectedPlan?.agentPlan.warnings ?? []"
      :expires-at="selectedPlan?.agentPlan.expiresAt"
      :busy="applyRunner.busy.value"
      :approve-label="approveLabel"
      @approve="approve"
      @regenerate="regenerate"
      @close="closeReview"
    />

    <AppConfirmDialog
      :open="!!confirmApp"
      title="Uninstall application"
      :confirm-label="`Uninstall ${confirmApp?.label ?? ''}`"
      tone="danger"
      :busy="runner.busy.value"
      @confirm="confirmUninstall"
      @close="confirmApp = undefined"
    >
      Removing <strong>{{ confirmApp?.label }}</strong> purges its packages. Sites or services that depend on it may stop
      working. You'll review the exact steps before anything changes.
    </AppConfirmDialog>
  </section>
</template>
