<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

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

import {
  applyPlan as applyToolPlan,
  getPlan as getToolPlan,
  prepareChange as prepareToolChange,
  type AdminToolPlan,
  type ToolAction,
  type ToolKind,
} from '@/modules/admintools/api'
import { useIdentityStore } from '@/modules/identity/store'

import { applyPlan, getPlan, listApplications, prepareChange, type Application, type ApplicationPlan, type AppAction } from '../api'

const appsQuery = useQuery({ queryKey: ['applications'], queryFn: listApplications, retry: false })
const identity = useIdentityStore()
const canWriteApplications = computed(() => identity.can('applications.write'))
const canWriteDatabases = computed(() => identity.can('databases.write'))
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
  return canWriteApplications.value && app.managed && (app.status === 'available' || app.status === 'failed')
}
function canUninstall(app: Application) {
  return canWriteApplications.value && app.managed && app.status === 'installed'
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
  if (!canWriteApplications.value) return
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
  if (!canWriteApplications.value) return
  confirmApp.value = app
}

function confirmUninstall() {
  const app = confirmApp.value
  confirmApp.value = undefined
  if (app && canWriteApplications.value) void prepare(app, 'package.remove')
}

function closeReview() {
  selectedPlan.value = undefined
  selected.value = undefined
}

function regenerate() {
  if (!canWriteApplications.value) return
  const app = selected.value
  const operation = selectedPlan.value?.operation
  if (!app || !operation) return
  selectedPlan.value = undefined
  void prepare(app, operation)
}

async function approve() {
  if (!identity.can('operations.apply')) return
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

// --- Database web clients (phpMyAdmin / pgAdmin) ---
// The admin-tools interface hides the runtime choice: phpMyAdmin is native on
// Nginx/PHP-FPM, while pgAdmin is an isolated on-demand container. Install =
// deploy the tool, Remove = stop its private route/runtime.

const toolPendingId = ref<string>()
const toolSelected = ref<Application>()
const toolPlan = ref<AdminToolPlan>()

function isWebClient(app: Application) {
  return app.category === 'web-client'
}
function canDeployTool(app: Application) {
  return canWriteDatabases.value && isWebClient(app) && (app.status === 'stopped' || app.status === 'inactive' || app.status === 'failed')
}
function canStopTool(app: Application) {
  return canWriteDatabases.value && isWebClient(app) && app.status === 'active'
}
function canReviewTool(app: Application) {
  return isWebClient(app) && app.status === 'plan_ready'
}

const toolReviewFacts = computed<Fact[]>(() => {
  const app = toolSelected.value
  const plan = toolPlan.value
  if (!app || !plan) return []
  return [
    { label: 'Web client', value: app.label },
    { label: 'Action', value: plan.operation === 'tool.stop' ? 'Uninstall' : 'Install' },
  ]
})

// The card buttons read Install/Uninstall, so the review dialog must too: the
// underlying tool.deploy/tool.stop lifecycle is an implementation detail here.
const toolApproveLabel = computed(() => (toolPlan.value?.operation === 'tool.stop' ? 'Uninstall' : 'Install'))

async function loadToolPlan(app: Application) {
  toolSelected.value = app
  planError.value = ''
  toolPlan.value = undefined
  try {
    toolPlan.value = (await getToolPlan(app.app as ToolKind)).plan
  } catch (caught) {
    planError.value = caught instanceof Error ? caught.message : 'The plan is not ready yet.'
  }
}

async function prepareTool(app: Application, action: ToolAction) {
  if (!canWriteDatabases.value) return
  toolPendingId.value = app.id
  await runner.run(async () => (await prepareToolChange(app.app as ToolKind, action)).job.id, {
    onSettled: async () => {
      await appsQuery.refetch()
    },
    onSuccess: async () => {
      await loadToolPlan(app)
    },
    failureMessage: `Preparing the ${app.label} change failed`,
  })
  toolPendingId.value = undefined
}

function closeToolReview() {
  toolPlan.value = undefined
  toolSelected.value = undefined
}

function regenerateTool() {
  if (!canWriteDatabases.value) return
  const app = toolSelected.value
  const operation = toolPlan.value?.operation
  if (!app || !operation) return
  toolPlan.value = undefined
  void prepareTool(app, operation)
}

async function approveTool() {
  if (!identity.can('operations.apply')) return
  const app = toolSelected.value
  if (!app) return
  const stopping = toolPlan.value?.operation === 'tool.stop'
  await applyRunner.run(async () => (await applyToolPlan(app.app as ToolKind)).id, {
    onSettled: async () => {
      closeToolReview()
      await appsQuery.refetch()
    },
    successToast: `${app.label} ${stopping ? 'stopped' : 'deployed'}`,
    failureMessage: `The ${app.label} operation failed`,
  })
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Software catalog"
      title="Applications"
      description="Install and remove server software — PHP versions, PostgreSQL, Node.js, Composer, and the phpMyAdmin and pgAdmin database web clients — through reviewed, agent-signed plans. Once a web client is installed, open it from the launch icon on any database row."
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
                <!-- Database web clients: use the shared lifecycle flow regardless of native/container runtime. -->
                <AppButton
                  v-if="canDeployTool(app)"
                  variant="primary"
                  icon="download"
                  :loading="toolPendingId === app.id"
                  :disabled="anyBusy"
                  @click="prepareTool(app, 'tool.deploy')"
                >
                  Install
                </AppButton>
                <AppButton v-if="canReviewTool(app)" icon="eye" :disabled="anyBusy" @click="loadToolPlan(app)">
                  Review plan
                </AppButton>
                <AppButton
                  v-if="canStopTool(app)"
                  variant="danger"
                  icon="stop"
                  :loading="toolPendingId === app.id"
                  :disabled="anyBusy"
                  @click="prepareTool(app, 'tool.stop')"
                >
                  Uninstall
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
      :can-approve="identity.can('operations.apply')"
      :can-regenerate="canWriteApplications"
      :approve-label="approveLabel"
      @approve="approve"
      @regenerate="regenerate"
      @close="closeReview"
    />

    <PlanReviewDialog
      :open="!!toolPlan"
      :title="toolSelected ? `Review ${toolSelected.label} plan` : 'Review plan'"
      :facts="toolReviewFacts"
      :steps="toolPlan?.agentPlan.steps ?? []"
      :warnings="toolPlan?.agentPlan.warnings ?? []"
      :expires-at="toolPlan?.agentPlan.expiresAt"
      :busy="applyRunner.busy.value"
      :can-approve="identity.can('operations.apply')"
      :can-regenerate="canWriteDatabases"
      :approve-label="toolApproveLabel"
      @approve="approveTool"
      @regenerate="regenerateTool"
      @close="closeToolReview"
    />

    <AppConfirmDialog
      :open="canWriteApplications && !!confirmApp"
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
