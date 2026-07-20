<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner, type JobMessage } from '@/shared/composables/useJobRunner'
import { usePlanReview, type PlanTarget } from '@/shared/composables/usePlanReview'
import { formatDateTime, formatMeasuredBytes } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppIcon,
  AppInput,
  AppSelect,
  CredentialReveal,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  EmptyState,
  FormField,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  PlanReviewDialog,
  SkeletonRow,
  StatusPill,
  TablePager,
  type Fact,
} from '@/shared/ui'

import { useToolLaunch } from '@/modules/admintools/composables/useToolLaunch'
import { useIdentityStore } from '@/modules/identity/store'

import {
  applyPlan,
  createBackup,
  createDatabase,
  createInstance,
  createRole,
  dropDatabase,
  dropRole,
  getPlan,
  listDatabases,
  listInstances,
  listRoles,
  revealCredential,
  rotateRole,
  type DatabaseRole,
  type ManagedDatabase,
  type PostgresPlan,
  type ResourceType,
} from '../api'

const NAME_RULE = /^[a-z][a-z0-9_]+$/
const NAME_HINT = 'Lowercase letters, numbers, and underscores; starts with a letter.'
const NAME_ERROR = 'Use lowercase letters, numbers, and underscores, starting with a letter (at least 2 characters).'

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))
const canApply = computed(() => identity.can('operations.apply'))

const instancesQuery = useQuery({ queryKey: ['postgresql-instances'], queryFn: listInstances, retry: false })
const rolesQuery = useQuery({ queryKey: ['postgresql-roles'], queryFn: listRoles, retry: false })
const databasesQuery = useQuery({ queryKey: ['postgresql-databases'], queryFn: listDatabases, retry: false })

const instances = computed(() => instancesQuery.data.value ?? [])
const roles = computed(() => rolesQuery.data.value ?? [])
const databases = computed(() => databasesQuery.data.value ?? [])
const activeInstances = computed(() => instances.value.filter((item) => item.status === 'active' || item.status === 'online'))
const activeRoles = computed(() => roles.value.filter((item) => item.status === 'active'))

// One runner per operation so only the form or button that launched a job shows
// busy while everything else stays usable.
const instanceRunner = useJobRunner()
const roleRunner = useJobRunner()
const databaseRunner = useJobRunner()
const rotateRunner = useJobRunner()
const backupRunner = useJobRunner()
const dropRunner = useJobRunner()

type Runner = ReturnType<typeof useJobRunner>

function failureProps(runner: Runner): { message: string; jobId?: number } {
  const props: { message: string; jobId?: number } = { message: runner.error.value }
  if (runner.progress.value && runner.jobId.value !== undefined) props.jobId = runner.jobId.value
  return props
}

function progressProps(runner: Runner): { messages: JobMessage[]; startedAtMs?: number } {
  const props: { messages: JobMessage[]; startedAtMs?: number } = { messages: runner.messages.value }
  if (runner.startedAtMs.value !== undefined) props.startedAtMs = runner.startedAtMs.value
  return props
}

async function refreshAll() {
  await Promise.all([instancesQuery.refetch(), rolesQuery.refetch(), databasesQuery.refetch()])
}

const plans = usePlanReview<ResourceType, PostgresPlan>({
  loadPlan: getPlan,
  applyPlan,
  refresh: refreshAll,
  canApply: () => identity.can('operations.apply'),
  isDestructive: (target, plan) =>
    plan.operation.toLowerCase().includes('drop') ||
    plan.operation.toLowerCase().includes('revoke') ||
    (target.type === 'restore-points' && (plan.operation.toLowerCase().includes('restore') || plan.agentPlan.interruption)),
})

// One-click pgAdmin launch, logged in as the database's owner role.
const toolLaunch = useToolLaunch()

function openPgAdmin(database: ManagedDatabase) {
  if (!identity.can('operations.apply')) return
  void toolLaunch.launch('pgadmin', 'postgresql', database.id)
}

function roleLabel(id: string) {
  return roles.value.find((role) => role.id === id)?.name ?? id
}

function instanceLabel(id: string) {
  const instance = instances.value.find((item) => item.id === id)
  return instance ? `PostgreSQL ${instance.version} · ${instance.cluster}` : id
}

/** Compact form for the table, where the full label would crowd the row. */
function instanceShort(id: string) {
  const instance = instances.value.find((item) => item.id === id)
  return instance ? `${instance.cluster} · v${instance.version}` : id
}

function detailLink(database: ManagedDatabase) {
  return `/databases/${encodeURIComponent(database.id)}`
}

/** Sizes are measured on read with a refresh interval, so say when, not just what. */
function sizeTitle(database: ManagedDatabase) {
  return database.sizeObservedAt ? `Measured ${formatDateTime(database.sizeObservedAt)}` : undefined
}

// --- The databases table ---

const collection = useCollection(() => databases.value, {
  searchText: (item) => `${item.name} ${roleLabel(item.ownerRoleId)} ${instanceShort(item.instanceId)}`,
  pageSize: 10,
})

// --- Create dialogs ---

const showInstanceDialog = ref(false)
const showRoleDialog = ref(false)
const showDatabaseDialog = ref(false)

const version = ref<'16' | '17' | '18'>('18')
const cluster = ref('nexa_main')
const port = ref<number | undefined>()
const instanceAttempted = ref(false)

const roleInstance = ref('')
const roleName = ref('')
const roleAttempted = ref(false)

const databaseInstance = ref('')
const databaseName = ref('')
const ownerRoleId = ref('')
const databaseAttempted = ref(false)

const clusterError = computed(() => (instanceAttempted.value && !NAME_RULE.test(cluster.value) ? NAME_ERROR : ''))
const portError = computed(() => {
  if (!instanceAttempted.value || typeof port.value !== 'number') return ''
  return port.value >= 1024 && port.value <= 65535 ? '' : 'Use a port between 1024 and 65535, or leave it empty.'
})

const roleInstanceError = computed(() => (roleAttempted.value && !roleInstance.value ? 'Select an instance.' : ''))
const roleNameError = computed(() => (roleAttempted.value && !NAME_RULE.test(roleName.value) ? NAME_ERROR : ''))

const databaseInstanceError = computed(() => (databaseAttempted.value && !databaseInstance.value ? 'Select an instance.' : ''))
const databaseNameError = computed(() => (databaseAttempted.value && !NAME_RULE.test(databaseName.value) ? NAME_ERROR : ''))
const ownerRoleError = computed(() => (databaseAttempted.value && !ownerRoleId.value ? 'Select an owner role.' : ''))

const ownerOptions = computed(() => activeRoles.value.filter((role) => role.instanceId === databaseInstance.value))
const ownerEmptyMessage = computed(() =>
  databaseInstance.value ? 'No roles on this instance — create one first' : 'Select an instance first',
)

function openInstanceDialog() {
  if (!canWrite.value) return
  instanceAttempted.value = false
  instanceRunner.progress.value = undefined
  instanceRunner.error.value = ''
  showInstanceDialog.value = true
}

function openRoleDialog() {
  if (!canWrite.value) return
  roleAttempted.value = false
  roleRunner.progress.value = undefined
  roleRunner.error.value = ''
  showRoleDialog.value = true
}

function openDatabaseDialog() {
  if (!canWrite.value) return
  databaseAttempted.value = false
  databaseRunner.progress.value = undefined
  databaseRunner.error.value = ''
  showDatabaseDialog.value = true
  // Round-trip the deep-link param so the URL always mirrors the dialog.
  if (route.query.create !== '1') void router.replace({ query: { ...route.query, create: '1' } })
}

/** Awaitable so follow-up navigation sees the query without ?create. */
async function closeDatabaseDialog() {
  showDatabaseDialog.value = false
  if ('create' in route.query) {
    const query = { ...route.query }
    delete query.create
    await router.replace({ query })
  }
}

function resetInstanceForm() {
  version.value = '18'
  cluster.value = 'nexa_main'
  port.value = undefined
  instanceAttempted.value = false
}

function resetRoleForm() {
  roleInstance.value = ''
  roleName.value = ''
  roleAttempted.value = false
}

function resetDatabaseForm() {
  databaseInstance.value = ''
  databaseName.value = ''
  ownerRoleId.value = ''
  databaseAttempted.value = false
}

async function submitInstance() {
  if (!canWrite.value) return
  instanceAttempted.value = true
  if (clusterError.value || portError.value) return
  let created: PlanTarget<ResourceType> | undefined
  await instanceRunner.run(
    async () => {
      const result = await createInstance({
        version: version.value,
        cluster: cluster.value,
        ...(typeof port.value === 'number' ? { port: port.value } : {}),
      })
      created = { type: 'instances', id: result.instance.id, label: `PostgreSQL ${version.value} · ${cluster.value}` }
      return result.job.id
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        showInstanceDialog.value = false
        resetInstanceForm()
        if (created) await plans.open(created)
      },
      failureMessage: 'Provisioning the instance failed',
    },
  )
}

async function submitRole() {
  if (!canWrite.value) return
  roleAttempted.value = true
  if (roleInstanceError.value || roleNameError.value) return
  let created: PlanTarget<ResourceType> | undefined
  await roleRunner.run(
    async () => {
      const result = await createRole(roleInstance.value, roleName.value)
      created = { type: 'roles', id: result.role.id, label: result.role.name }
      return result.job.id
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        showRoleDialog.value = false
        resetRoleForm()
        if (created) await plans.open(created)
      },
      failureMessage: 'Creating the role failed',
    },
  )
}

async function submitDatabase() {
  if (!canWrite.value) return
  databaseAttempted.value = true
  if (databaseInstanceError.value || databaseNameError.value || ownerRoleError.value) return
  let created: PlanTarget<ResourceType> | undefined
  await databaseRunner.run(
    async () => {
      const result = await createDatabase({
        instanceId: databaseInstance.value,
        name: databaseName.value,
        ownerRoleId: ownerRoleId.value,
      })
      created = { type: 'databases', id: result.database.id, label: result.database.name }
      return result.job.id
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        await closeDatabaseDialog()
        resetDatabaseForm()
        if (created) await plans.open(created)
      },
      failureMessage: 'Creating the database failed',
    },
  )
}

// --- Plan facts ---

const planFacts = computed<Fact[]>(() => {
  const resource = plans.target.value
  if (!resource) return []
  switch (resource.type) {
    case 'instances': {
      const instance = instances.value.find((item) => item.id === resource.id)
      if (!instance) return []
      return [
        { label: 'Cluster', value: instance.cluster, mono: true },
        { label: 'Version', value: `PostgreSQL ${instance.version}` },
        { label: 'Port', value: String(instance.port), mono: true },
        { label: 'Data path', value: instance.dataPath, mono: true },
      ]
    }
    case 'roles': {
      const role = roles.value.find((item) => item.id === resource.id)
      if (!role) return []
      return [
        { label: 'Role', value: role.name, mono: true },
        { label: 'Instance', value: instanceLabel(role.instanceId) },
        { label: 'Credential version', value: `v${role.credentialVersion}` },
      ]
    }
    case 'databases': {
      const database = databases.value.find((item) => item.id === resource.id)
      if (!database) return []
      return [
        { label: 'Database', value: database.name, mono: true },
        { label: 'Owner role', value: roleLabel(database.ownerRoleId) },
        { label: 'Instance', value: instanceLabel(database.instanceId) },
      ]
    }
  }
  return []
})

// --- Row actions ---

const rotateTarget = ref<DatabaseRole>()
const rotatePendingId = ref<string>()
const backupPendingId = ref<string>()

// Clear each pending row marker once its runner settles so a stale id can never
// light up a row when the runner becomes busy again later.
watch(rotateRunner.busy, (busy) => {
  if (!busy) rotatePendingId.value = undefined
})
watch(backupRunner.busy, (busy) => {
  if (!busy) backupPendingId.value = undefined
})

function confirmRotate() {
  const role = rotateTarget.value
  if (!role || !canWrite.value) return
  rotateTarget.value = undefined
  rotatePendingId.value = role.id
  clearCredential()
  void rotateRunner.run(async () => (await rotateRole(role.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => plans.open({ type: 'roles', id: role.id, label: `Rotate ${role.name}` }),
    failureMessage: 'Rotating the credential failed',
  })
}

function backupDatabase(database: ManagedDatabase) {
  if (!canWrite.value) return
  backupPendingId.value = database.id
  void backupRunner.run(async () => (await createBackup(database.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Backed up ${database.name}`,
    failureMessage: 'The backup failed',
  })
}

// --- Delete (drop) ---
// A drop follows the same review path: the DELETE request produces a plan-ready
// resource, then the destructive plan is opened for a final approval before the
// database or role is actually removed.
const dropDatabaseTarget = ref<ManagedDatabase>()
const dropRoleTarget = ref<DatabaseRole>()

function confirmDropDatabase() {
  const database = dropDatabaseTarget.value
  if (!database || !canWrite.value) return
  dropDatabaseTarget.value = undefined
  void dropRunner.run(async () => (await dropDatabase(database.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => plans.open({ type: 'databases', id: database.id, label: `Delete ${database.name}` }),
    failureMessage: 'Deleting the database failed',
  })
}

function confirmDropRole() {
  const role = dropRoleTarget.value
  if (!role || !canWrite.value) return
  dropRoleTarget.value = undefined
  void dropRunner.run(async () => (await dropRole(role.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => plans.open({ type: 'roles', id: role.id, label: `Delete ${role.name}` }),
    failureMessage: 'Deleting the role failed',
  })
}

// --- One-time credential reveal ---

const revealTarget = ref<DatabaseRole>()
const revealBusy = ref(false)
const revealError = ref('')
const credential = ref('')
const revealedRole = ref<DatabaseRole>()
const credentialCardEl = ref<HTMLElement>()

function askReveal(role: DatabaseRole) {
  if (!canApply.value) return
  revealError.value = ''
  revealTarget.value = role
}

function clearCredential() {
  credential.value = ''
  revealedRole.value = undefined
}

async function confirmReveal() {
  const role = revealTarget.value
  if (!role || !canApply.value) return
  revealBusy.value = true
  revealError.value = ''
  try {
    credential.value = await revealCredential(role.id)
    revealedRole.value = role
    revealTarget.value = undefined
    await rolesQuery.refetch()
    credentialCardEl.value?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  } catch (caught) {
    revealError.value = caught instanceof Error ? caught.message : 'The credential could not be revealed.'
  } finally {
    revealBusy.value = false
  }
}

const revealFacts = computed<Fact[]>(() => {
  const role = revealedRole.value
  if (!role) return []
  const instance = instances.value.find((item) => item.id === role.instanceId)
  const owned = databases.value.filter((item) => item.ownerRoleId === role.id)
  const facts: Fact[] = []
  if (instance) {
    facts.push({ label: 'Instance', value: `PostgreSQL ${instance.version} · ${instance.cluster}` })
    if (instance.port) facts.push({ label: 'Port', value: String(instance.port), mono: true })
    else facts.push({ label: 'Socket', value: instance.socketPath, mono: true })
  }
  if (owned.length) {
    facts.push({
      label: owned.length === 1 ? 'Owns database' : 'Owns databases',
      value: owned.map((database) => database.name).join(', '),
    })
  }
  return facts
})

watch(
  () => route.query.create,
  (value) => {
    if (value !== '1') return
    if (canWrite.value) openDatabaseDialog()
    else void closeDatabaseDialog()
  },
  { immediate: true },
)

watch(databaseInstance, () => {
  if (ownerRoleId.value && !ownerOptions.value.some((role) => role.id === ownerRoleId.value)) ownerRoleId.value = ''
})
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Managed data layer"
      title="PostgreSQL"
      description="Every change is planned first and runs only after you approve it. Passwords are shown exactly once after creation or rotation."
    >
      <AppButton v-if="canWrite" icon="plus" @click="openInstanceDialog">Provision instance</AppButton>
      <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openDatabaseDialog">New database</AppButton>
    </PageHeader>

    <AppAlert v-if="!canWrite" tone="info">Your account has read-only access to PostgreSQL resources.</AppAlert>

    <JobFailureNotice v-if="plans.applyRunner.error.value" v-bind="failureProps(plans.applyRunner)" />
    <JobProgress
      v-if="plans.applyRunner.progress.value"
      :event="plans.applyRunner.progress.value"
      v-bind="progressProps(plans.applyRunner)"
    />

    <div v-if="credential" ref="credentialCardEl" class="rounded-2xl">
      <CredentialReveal
        :credential="credential"
        :account-label="revealedRole?.name ?? ''"
        :facts="revealFacts"
        @clear="clearCredential"
      />
    </div>

    <!-- Instances: a summary strip rather than a section, because the databases
         table below is what this page is actually for. -->
    <div v-if="instancesQuery.isPending.value" class="space-y-1">
      <SkeletonRow v-for="index in 2" :key="index" />
    </div>
    <AppAlert v-else-if="instancesQuery.isError.value" tone="danger">
      <div class="flex flex-wrap items-center gap-3">
        <span class="min-w-0 flex-1">Instances could not be loaded.</span>
        <AppButton size="sm" @click="instancesQuery.refetch()">Retry</AppButton>
      </div>
    </AppAlert>
    <EmptyState
      v-else-if="!instances.length"
      icon="database"
      title="No instances yet"
      description="Create one to host roles and databases. Each instance gets its own port, data path, and systemd unit."
    >
      <template v-if="canWrite" #action>
        <AppButton icon="plus" @click="openInstanceDialog">Provision instance</AppButton>
      </template>
    </EmptyState>
    <div v-else class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <div
        v-for="item in instances"
        :key="item.id"
        class="flex items-center gap-3 rounded-xl border border-outline bg-surface/80 px-3.5 py-3"
      >
        <span
          class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-[13px] font-bold text-accent-300"
        >
          {{ item.version }}
        </span>
        <span class="min-w-0 flex-1">
          <strong class="block truncate text-[13px] font-semibold text-ink">{{ item.cluster }}</strong>
          <small class="block truncate font-mono text-[11px] text-ink-muted">Port {{ item.port }}</small>
        </span>
        <AppButton
          v-if="item.status === 'plan_ready'"
          size="sm"
          :loading="plans.loadingId.value === item.id"
          @click="plans.open({ type: 'instances', id: item.id, label: instanceLabel(item.id) })"
        >
          Review
        </AppButton>
        <StatusPill v-else :status="item.status" />
      </div>
    </div>

    <!-- Databases: the hero. -->
    <AppCard flush eyebrow="Owned resources" title="Databases">
      <template #actions>
        <AppButton v-if="canWrite" size="sm" icon="plus" @click="openDatabaseDialog">New database</AppButton>
      </template>
      <div class="space-y-3 px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="backupRunner.error.value || backupRunner.progress.value" class="space-y-2">
          <JobFailureNotice v-if="backupRunner.error.value" v-bind="failureProps(backupRunner)" />
          <JobProgress
            v-if="backupRunner.progress.value"
            :event="backupRunner.progress.value"
            v-bind="progressProps(backupRunner)"
          />
        </div>
        <AppAlert v-if="toolLaunch.toolsQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center gap-3">
            <span class="min-w-0 flex-1">The pgAdmin deployment status could not be loaded.</span>
            <AppButton size="sm" @click="toolLaunch.toolsQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <AppAlert v-if="toolLaunch.error.value" tone="danger">{{ toolLaunch.error.value }}</AppAlert>
        <AppAlert v-if="toolLaunch.blocked.value" tone="info">
          <p>The browser blocked the pgAdmin tab.</p>
          <a
            :href="toolLaunch.blocked.value.url"
            target="_blank"
            rel="noopener"
            class="mt-1 inline-flex items-center gap-1.5 font-medium underline underline-offset-2"
          >
            Open pgAdmin
            <AppIcon name="external-link" :size="14" />
          </a>
        </AppAlert>
        <div v-if="databasesQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="index in 3" :key="index" />
        </div>
        <AppAlert v-else-if="databasesQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center gap-3">
            <span class="min-w-0 flex-1">Databases could not be loaded.</span>
            <AppButton size="sm" @click="databasesQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <EmptyState
          v-else-if="!databases.length"
          icon="database"
          title="No databases yet"
          description="Create a database on an active instance, owned by a role."
        >
          <template v-if="canWrite" #action>
            <AppButton icon="plus" @click="openDatabaseDialog">New database</AppButton>
          </template>
        </EmptyState>
        <template v-else>
          <ListToolbar
            v-model:search="collection.search.value"
            :count="collection.matching.value"
            count-label="databases"
            placeholder="Search by name, owner, or instance"
          />
          <EmptyState
            v-if="!collection.items.value.length"
            icon="search"
            title="No matching databases"
            description="No databases match your search. Clear it to see every database."
          />
          <template v-else>
            <div class="overflow-x-auto">
              <table class="w-full border-collapse text-left">
                <thead>
                  <tr class="border-b border-outline">
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Name</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Instance</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Owner</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Size</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Created</th>
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                    <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-outline">
                  <tr v-for="item in collection.items.value" :key="item.id">
                    <td class="max-w-[14rem] px-3 py-2.5">
                      <RouterLink
                        :to="detailLink(item)"
                        class="block truncate font-mono text-[13px] font-semibold text-ink transition-colors hover:text-accent-300"
                        :title="item.name"
                      >
                        {{ item.name }}
                      </RouterLink>
                    </td>
                    <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                      {{ instanceShort(item.instanceId) }}
                    </td>
                    <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary">
                      {{ roleLabel(item.ownerRoleId) }}
                    </td>
                    <td
                      class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary tabular-nums"
                      :class="item.sizeObservedAt ? 'cursor-help' : ''"
                      :title="sizeTitle(item)"
                    >
                      {{ formatMeasuredBytes(item.sizeBytes) }}
                    </td>
                    <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                      {{ formatDateTime(item.createdAt) }}
                    </td>
                    <td class="px-3 py-2.5"><StatusPill :status="item.status" /></td>
                    <td class="px-3 py-2.5 text-right">
                      <span class="flex items-center justify-end gap-1">
                        <AppButton
                          v-if="item.status === 'active' && canApply"
                          size="sm"
                          variant="ghost"
                          icon="external-link"
                          :loading="toolLaunch.launchingId.value === item.id"
                          :disabled="toolLaunch.availability('pgadmin') !== 'ready'"
                          :aria-label="`Open pgAdmin for ${item.name}`"
                          :title="
                            toolLaunch.availability('pgadmin') === 'ready'
                              ? `Open pgAdmin for ${item.name}`
                              : toolLaunch.availability('pgadmin') === 'loading'
                                ? 'Checking pgAdmin status'
                                : toolLaunch.availability('pgadmin') === 'error'
                                  ? 'pgAdmin status is unavailable'
                                  : 'Install pgAdmin from the Applications page to enable this'
                          "
                          @click="openPgAdmin(item)"
                        />
                        <DropdownMenu>
                          <DropdownMenuTrigger as-child>
                            <AppButton
                              size="sm"
                              variant="ghost"
                              icon="more-horizontal"
                              :aria-label="`Actions for ${item.name}`"
                            />
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem
                              v-if="item.status === 'plan_ready'"
                              @select="plans.open({ type: 'databases', id: item.id, label: item.name })"
                            >
                              Review plan…
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              v-if="canWrite && item.status === 'active'"
                              :disabled="backupRunner.busy.value"
                              @select="backupDatabase(item)"
                            >
                              Back up now
                            </DropdownMenuItem>
                            <DropdownMenuSeparator
                              v-if="item.status === 'plan_ready' || (canWrite && item.status === 'active')"
                            />
                            <DropdownMenuItem as-child>
                              <RouterLink :to="detailLink(item)">Access and backups</RouterLink>
                            </DropdownMenuItem>
                            <template v-if="canWrite && (item.status === 'active' || item.status === 'failed')">
                              <DropdownMenuSeparator />
                              <DropdownMenuItem
                                class="text-rose-300"
                                :disabled="dropRunner.busy.value"
                                @select="dropDatabaseTarget = item"
                              >
                                Delete database…
                              </DropdownMenuItem>
                            </template>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <TablePager
              v-model:page="collection.page.value"
              v-model:page-size="collection.pageSize.value"
              :page-count="collection.pageCount.value"
              :total="collection.matching.value"
              :range-start="collection.rangeStart.value"
              :range-end="collection.rangeEnd.value"
              label="databases"
            />
          </template>
        </template>
      </div>
    </AppCard>

    <!-- Roles stay on this page rather than moving to the per-database
         drill-down: a database cannot be created without an existing owner
         role, so roles reachable only from a database would deadlock the very
         first one. -->
    <AppCard flush eyebrow="Login identities" title="Roles">
      <template #actions>
        <AppButton v-if="canWrite" size="sm" icon="plus" @click="openRoleDialog">Create role</AppButton>
      </template>
      <div class="space-y-3 px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="rotateRunner.error.value || rotateRunner.progress.value" class="space-y-2">
          <JobFailureNotice v-if="rotateRunner.error.value" v-bind="failureProps(rotateRunner)" />
          <JobProgress
            v-if="rotateRunner.progress.value"
            :event="rotateRunner.progress.value"
            v-bind="progressProps(rotateRunner)"
          />
        </div>
        <div v-if="rolesQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="index in 2" :key="index" />
        </div>
        <AppAlert v-else-if="rolesQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center gap-3">
            <span class="min-w-0 flex-1">Roles could not be loaded.</span>
            <AppButton size="sm" @click="rolesQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <EmptyState
          v-else-if="!roles.length"
          icon="key"
          title="No roles yet"
          description="Roles are the login identities that own and access databases. Create one on an active instance."
        >
          <template v-if="canWrite" #action>
            <AppButton icon="plus" @click="openRoleDialog">Create role</AppButton>
          </template>
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <table class="w-full border-collapse text-left">
            <thead>
              <tr class="border-b border-outline">
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Role</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Instance</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Credential</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-outline">
              <tr v-for="role in roles" :key="role.id">
                <td class="px-3 py-2.5 font-mono text-[13px] font-medium whitespace-nowrap text-ink">{{ role.name }}</td>
                <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                  {{ instanceShort(role.instanceId) }}
                </td>
                <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary tabular-nums">
                  v{{ role.credentialVersion }}
                </td>
                <td class="px-3 py-2.5"><StatusPill :status="role.status" /></td>
                <td class="px-3 py-2.5 text-right">
                  <span class="flex items-center justify-end gap-1">
                    <AppButton
                      v-if="role.status === 'plan_ready'"
                      size="sm"
                      :loading="plans.loadingId.value === role.id"
                      @click="plans.open({ type: 'roles', id: role.id, label: role.name })"
                    >
                      Review
                    </AppButton>
                    <AppButton v-if="role.credentialAvailable && canApply" size="sm" icon="key" @click="askReveal(role)">
                      Reveal once
                    </AppButton>
                    <AppButton
                      v-if="canWrite && role.status === 'active'"
                      size="sm"
                      variant="ghost"
                      icon="refresh-cw"
                      :aria-label="`Rotate ${role.name}`"
                      :loading="rotateRunner.busy.value && rotatePendingId === role.id"
                      :disabled="rotateRunner.busy.value"
                      @click="rotateTarget = role"
                    />
                    <AppButton
                      v-if="canWrite && (role.status === 'active' || role.status === 'failed')"
                      size="sm"
                      variant="ghost"
                      icon="trash"
                      :aria-label="`Delete ${role.name}`"
                      :disabled="dropRunner.busy.value"
                      @click="dropRoleTarget = role"
                    />
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </AppCard>

    <!-- Create dialogs -->
    <AppDialog :open="canWrite && showInstanceDialog" title="Provision instance" @close="showInstanceDialog = false">
      <form class="space-y-4" novalidate @submit.prevent="submitInstance">
        <FormField label="Version">
          <AppSelect v-model="version">
            <option value="16">PostgreSQL 16</option>
            <option value="17">PostgreSQL 17</option>
            <option value="18">PostgreSQL 18</option>
          </AppSelect>
        </FormField>
        <FormField label="Cluster name" :hint="NAME_HINT" :error="clusterError">
          <AppInput v-model="cluster" autocomplete="off" spellcheck="false" :invalid="!!clusterError" />
        </FormField>
        <FormField label="Port" hint="Leave empty to assign a free port automatically." :error="portError">
          <AppInput v-model.number="port" type="number" min="1024" max="65535" :invalid="!!portError" />
        </FormField>
        <JobFailureNotice v-if="instanceRunner.error.value" v-bind="failureProps(instanceRunner)" />
        <JobProgress
          v-if="instanceRunner.progress.value"
          :event="instanceRunner.progress.value"
          v-bind="progressProps(instanceRunner)"
        />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <AppButton :disabled="instanceRunner.busy.value" @click="showInstanceDialog = false">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="instanceRunner.busy.value">Provision instance</AppButton>
        </div>
      </form>
    </AppDialog>

    <AppDialog :open="canWrite && showRoleDialog" title="Create role" @close="showRoleDialog = false">
      <form class="space-y-4" novalidate @submit.prevent="submitRole">
        <FormField label="Instance" :error="roleInstanceError">
          <AppSelect
            v-model="roleInstance"
            :invalid="!!roleInstanceError"
            empty-message="No active instances — provision one first"
          >
            <option v-if="activeInstances.length" disabled value="">Select instance</option>
            <option v-for="item in activeInstances" :key="item.id" :value="item.id">
              PostgreSQL {{ item.version }} · {{ item.cluster }}
            </option>
          </AppSelect>
        </FormField>
        <FormField label="Role name" :hint="NAME_HINT" :error="roleNameError">
          <AppInput v-model="roleName" autocomplete="off" spellcheck="false" :invalid="!!roleNameError" />
        </FormField>
        <JobFailureNotice v-if="roleRunner.error.value" v-bind="failureProps(roleRunner)" />
        <JobProgress
          v-if="roleRunner.progress.value"
          :event="roleRunner.progress.value"
          v-bind="progressProps(roleRunner)"
        />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <AppButton :disabled="roleRunner.busy.value" @click="showRoleDialog = false">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="roleRunner.busy.value">Create role</AppButton>
        </div>
      </form>
    </AppDialog>

    <AppDialog :open="canWrite && showDatabaseDialog" title="New database" @close="closeDatabaseDialog">
      <form class="space-y-4" novalidate @submit.prevent="submitDatabase">
        <FormField label="Instance" :error="databaseInstanceError">
          <AppSelect
            v-model="databaseInstance"
            :invalid="!!databaseInstanceError"
            empty-message="No active instances — provision one first"
          >
            <option v-if="activeInstances.length" disabled value="">Select instance</option>
            <option v-for="item in activeInstances" :key="item.id" :value="item.id">
              PostgreSQL {{ item.version }} · {{ item.cluster }}
            </option>
          </AppSelect>
        </FormField>
        <FormField label="Database name" :hint="NAME_HINT" :error="databaseNameError">
          <AppInput v-model="databaseName" autocomplete="off" spellcheck="false" :invalid="!!databaseNameError" />
        </FormField>
        <FormField label="Owner role" hint="The role that owns the database and its objects." :error="ownerRoleError">
          <AppSelect v-model="ownerRoleId" :invalid="!!ownerRoleError" :empty-message="ownerEmptyMessage">
            <option v-if="ownerOptions.length" disabled value="">Select owner</option>
            <option v-for="role in ownerOptions" :key="role.id" :value="role.id">{{ role.name }}</option>
          </AppSelect>
        </FormField>
        <JobFailureNotice v-if="databaseRunner.error.value" v-bind="failureProps(databaseRunner)" />
        <JobProgress
          v-if="databaseRunner.progress.value"
          :event="databaseRunner.progress.value"
          v-bind="progressProps(databaseRunner)"
        />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <AppButton :disabled="databaseRunner.busy.value" @click="closeDatabaseDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="databaseRunner.busy.value">Create database</AppButton>
        </div>
      </form>
    </AppDialog>

    <PlanReviewDialog
      :open="plans.dialogOpen.value"
      :title="plans.target.value?.label ?? 'Review plan'"
      :facts="planFacts"
      :warnings="plans.warnings.value"
      :busy="plans.busy.value || plans.applyRunner.busy.value"
      :can-approve="canApply"
      approve-label="Approve and execute"
      v-bind="plans.dialogProps.value"
      @approve="plans.apply()"
      @regenerate="plans.regenerate()"
      @close="plans.dialogOpen.value = false"
    />

    <AppConfirmDialog
      :open="canWrite && !!rotateTarget"
      :title="rotateTarget ? `Rotate credential for ${rotateTarget.name}?` : 'Rotate credential?'"
      confirm-label="Rotate credential"
      @confirm="confirmRotate"
      @close="rotateTarget = undefined"
    >
      Connected applications will fail until the new password is deployed. You review a plan before anything changes, and
      the new password is shown exactly once.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="canWrite && !!dropDatabaseTarget"
      :title="dropDatabaseTarget ? `Delete database ${dropDatabaseTarget.name}?` : 'Delete database?'"
      confirm-label="Delete database"
      tone="danger"
      @confirm="confirmDropDatabase"
      @close="dropDatabaseTarget = undefined"
    >
      This permanently deletes the database and everything in it. You review a final plan before it is removed, but there
      is no undo — back it up first if you might need the data.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="canWrite && !!dropRoleTarget"
      :title="dropRoleTarget ? `Delete role ${dropRoleTarget.name}?` : 'Delete role?'"
      confirm-label="Delete role"
      tone="danger"
      @confirm="confirmDropRole"
      @close="dropRoleTarget = undefined"
    >
      Applications signing in as this role will lose access. A database this role owns must keep at least one other role,
      or the deletion is refused; if another role remains, it inherits ownership.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="canApply && !!revealTarget"
      :title="revealTarget ? `Reveal credential for ${revealTarget.name}?` : 'Reveal credential?'"
      confirm-label="Reveal now"
      tone="accent"
      :busy="revealBusy"
      @confirm="confirmReveal"
      @close="revealTarget = undefined"
    >
      <p>This credential is shown exactly once. Copy or download it before leaving the page — it cannot be revealed again.</p>
      <AppAlert v-if="revealError" tone="danger" class="mt-3">{{ revealError }}</AppAlert>
    </AppConfirmDialog>
  </section>
</template>
