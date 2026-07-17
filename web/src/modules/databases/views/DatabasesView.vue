<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useJobRunner, type JobMessage } from '@/shared/composables/useJobRunner'
import { useToasts } from '@/shared/composables/useToasts'
import { formatBytes, formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppInput,
  AppSelect,
  CredentialReveal,
  EmptyState,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  PlanReviewDialog,
  ResourceRow,
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'

import {
  applyPlan,
  createBackup,
  createDatabase,
  createGrant,
  createInstance,
  createRole,
  getPlan,
  listDatabases,
  listGrants,
  listInstances,
  listRestorePoints,
  listRoles,
  prepareRestore,
  revealCredential,
  rotateRole,
  type AccessLevel,
  type DatabaseRole,
  type ManagedDatabase,
  type PostgresPlan,
  type ResourceType,
  type RestorePoint,
} from '../api'

interface SelectedResource {
  type: ResourceType
  id: string
  label: string
}

const NAME_RULE = /^[a-z][a-z0-9_]+$/
const NAME_HINT = 'Lowercase letters, numbers, and underscores; starts with a letter.'
const NAME_ERROR = 'Use lowercase letters, numbers, and underscores, starting with a letter (at least 2 characters).'

const ACCESS_LABELS: Record<AccessLevel, string> = {
  connect: 'Connect only',
  read_only: 'Read only',
  read_write: 'Read and write',
}

const route = useRoute()
const router = useRouter()
const toasts = useToasts()

const instancesQuery = useQuery({ queryKey: ['postgresql-instances'], queryFn: listInstances, retry: false })
const rolesQuery = useQuery({ queryKey: ['postgresql-roles'], queryFn: listRoles, retry: false })
const databasesQuery = useQuery({ queryKey: ['postgresql-databases'], queryFn: listDatabases, retry: false })
const grantsQuery = useQuery({ queryKey: ['postgresql-grants'], queryFn: listGrants, retry: false })
const restorePointsQuery = useQuery({ queryKey: ['postgresql-restore-points'], queryFn: listRestorePoints, retry: false })

const instances = computed(() => instancesQuery.data.value ?? [])
const roles = computed(() => rolesQuery.data.value ?? [])
const databases = computed(() => databasesQuery.data.value ?? [])
const grants = computed(() => grantsQuery.data.value ?? [])
const restorePoints = computed(() => restorePointsQuery.data.value ?? [])
const activeInstances = computed(() => instances.value.filter((item) => item.status === 'active' || item.status === 'online'))
const activeRoles = computed(() => roles.value.filter((item) => item.status === 'active'))
const activeDatabases = computed(() => databases.value.filter((item) => item.status === 'active'))

// One runner per operation so only the form or button that launched a job
// shows busy while everything else stays usable.
const instanceRunner = useJobRunner()
const roleRunner = useJobRunner()
const databaseRunner = useJobRunner()
const grantRunner = useJobRunner()
const rotateRunner = useJobRunner()
const backupRunner = useJobRunner()
const restoreRunner = useJobRunner()
const applyRunner = useJobRunner()

type Runner = ReturnType<typeof useJobRunner>

// exactOptionalPropertyTypes: include optional props only when they have values.
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

function roleLabel(id: string) {
  return roles.value.find((role) => role.id === id)?.name ?? id
}

function instanceLabel(id: string) {
  const instance = instances.value.find((item) => item.id === id)
  return instance ? `PostgreSQL ${instance.version} · ${instance.cluster}` : id
}

function databaseLabel(id: string) {
  return databases.value.find((database) => database.id === id)?.name ?? id
}

function selectedLink(id: string) {
  return `/databases?selected=${encodeURIComponent(id)}`
}

async function refreshAll() {
  await Promise.all([
    instancesQuery.refetch(),
    rolesQuery.refetch(),
    databasesQuery.refetch(),
    grantsQuery.refetch(),
    restorePointsQuery.refetch(),
  ])
}

// --- Selection (?selected=) ---

const selectedId = ref<string>()
const pendingScrollId = ref<string>()

function select(id: string) {
  selectedId.value = id
  if (route.query.selected !== id) void router.replace({ query: { ...route.query, selected: id } })
}

async function tryScrollToPending() {
  const id = pendingScrollId.value
  if (!id) return
  await nextTick()
  const row = document.getElementById(`resource-${id}`)
  if (!row) return
  row.scrollIntoView({ block: 'center', behavior: 'smooth' })
  pendingScrollId.value = undefined
}

// --- Create dialogs ---

const showInstanceDialog = ref(false)
const showRoleDialog = ref(false)
const showDatabaseDialog = ref(false)
const showGrantDialog = ref(false)

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

const grantDatabaseId = ref('')
const grantRoleId = ref('')
const access = ref<AccessLevel>('read_write')
const grantAttempted = ref(false)

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

const grantDatabaseError = computed(() => (grantAttempted.value && !grantDatabaseId.value ? 'Select a database.' : ''))
const grantRoleError = computed(() => (grantAttempted.value && !grantRoleId.value ? 'Select a role.' : ''))

const ownerOptions = computed(() => activeRoles.value.filter((role) => role.instanceId === databaseInstance.value))
const ownerEmptyMessage = computed(() =>
  databaseInstance.value ? 'No roles on this instance — create one first' : 'Select an instance first',
)

function openInstanceDialog() {
  instanceAttempted.value = false
  instanceRunner.progress.value = undefined
  instanceRunner.error.value = ''
  showInstanceDialog.value = true
}

function openRoleDialog() {
  roleAttempted.value = false
  roleRunner.progress.value = undefined
  roleRunner.error.value = ''
  showRoleDialog.value = true
}

function openDatabaseDialog() {
  databaseAttempted.value = false
  databaseRunner.progress.value = undefined
  databaseRunner.error.value = ''
  showDatabaseDialog.value = true
  // Round-trip the deep-link param so the URL always mirrors the dialog.
  if (route.query.create !== '1') void router.replace({ query: { ...route.query, create: '1' } })
}

function openGrantDialog() {
  grantAttempted.value = false
  grantRunner.progress.value = undefined
  grantRunner.error.value = ''
  showGrantDialog.value = true
}

/** Awaitable so follow-up navigation (e.g. select()) sees the query without ?create. */
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

function resetGrantForm() {
  grantDatabaseId.value = ''
  grantRoleId.value = ''
  access.value = 'read_write'
  grantAttempted.value = false
}

async function submitInstance() {
  instanceAttempted.value = true
  if (clusterError.value || portError.value) return
  let created: SelectedResource | undefined
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
        if (created) await openPlanReview(created)
      },
      failureMessage: 'Provisioning the instance failed',
    },
  )
}

async function submitRole() {
  roleAttempted.value = true
  if (roleInstanceError.value || roleNameError.value) return
  let created: SelectedResource | undefined
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
        if (created) await openPlanReview(created)
      },
      failureMessage: 'Creating the role failed',
    },
  )
}

async function submitDatabase() {
  databaseAttempted.value = true
  if (databaseInstanceError.value || databaseNameError.value || ownerRoleError.value) return
  let created: SelectedResource | undefined
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
        if (created) await openPlanReview(created)
      },
      failureMessage: 'Creating the database failed',
    },
  )
}

async function submitGrant() {
  grantAttempted.value = true
  if (grantDatabaseError.value || grantRoleError.value) return
  let created: SelectedResource | undefined
  await grantRunner.run(
    async () => {
      const result = await createGrant({ databaseId: grantDatabaseId.value, roleId: grantRoleId.value, access: access.value })
      created = {
        type: 'grants',
        id: result.grant.id,
        label: `${roleLabel(result.grant.roleId)} → ${databaseLabel(result.grant.databaseId)}`,
      }
      return result.job.id
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        showGrantDialog.value = false
        resetGrantForm()
        if (created) await openPlanReview(created)
      },
      failureMessage: 'Creating the grant failed',
    },
  )
}

// --- Plan review ---

const planResource = ref<SelectedResource>()
const plan = ref<PostgresPlan>()
const planExpiresAt = ref<string>()
const planDialogOpen = ref(false)
const planBusy = ref(false)
const planLoadingId = ref<string>()

async function openPlanReview(resource: SelectedResource) {
  planLoadingId.value = resource.id
  select(resource.id)
  try {
    const response = await getPlan(resource.type, resource.id)
    planResource.value = resource
    plan.value = response.plan
    planExpiresAt.value = response.expiresAt
    planDialogOpen.value = true
  } catch (caught) {
    toasts.push({
      title: 'Could not load the plan',
      body: caught instanceof Error ? caught.message : 'The plan is not ready yet.',
      tone: 'danger',
    })
  } finally {
    planLoadingId.value = undefined
  }
}

async function regeneratePlan() {
  const resource = planResource.value
  if (!resource) return
  planBusy.value = true
  try {
    const response = await getPlan(resource.type, resource.id)
    plan.value = response.plan
    planExpiresAt.value = response.expiresAt
  } catch (caught) {
    toasts.push({
      title: 'Could not regenerate the plan',
      body: caught instanceof Error ? caught.message : 'Try preparing the operation again.',
      tone: 'danger',
    })
  } finally {
    planBusy.value = false
  }
}

const planFacts = computed<Fact[]>(() => {
  const resource = planResource.value
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
        { label: 'Instance', value: instanceLabel(role.instanceId), to: selectedLink(role.instanceId) },
        { label: 'Credential version', value: `v${role.credentialVersion}` },
      ]
    }
    case 'databases': {
      const database = databases.value.find((item) => item.id === resource.id)
      if (!database) return []
      return [
        { label: 'Database', value: database.name, mono: true },
        { label: 'Owner role', value: roleLabel(database.ownerRoleId), to: selectedLink(database.ownerRoleId) },
        { label: 'Instance', value: instanceLabel(database.instanceId), to: selectedLink(database.instanceId) },
      ]
    }
    case 'grants': {
      const grant = grants.value.find((item) => item.id === resource.id)
      if (!grant) return []
      return [
        { label: 'Role', value: roleLabel(grant.roleId), to: selectedLink(grant.roleId) },
        { label: 'Database', value: databaseLabel(grant.databaseId), to: selectedLink(grant.databaseId) },
        { label: 'Access', value: ACCESS_LABELS[grant.access] },
      ]
    }
    case 'restore-points': {
      const point = restorePoints.value.find((item) => item.id === resource.id)
      if (!point) return []
      return [
        { label: 'Database', value: databaseLabel(point.databaseId), to: selectedLink(point.databaseId) },
        { label: 'Size', value: formatBytes(point.sizeBytes) },
        { label: 'Verified', value: point.verifiedAt ? formatDateTime(point.verifiedAt) : 'Not verified yet' },
      ]
    }
  }
  return []
})

const planWarnings = computed(() => {
  const current = plan.value
  if (!current) return []
  const warnings = [...current.agentPlan.warnings]
  if (current.agentPlan.interruption) warnings.push('Active connections are terminated while this plan is applied.')
  return warnings
})

const planDialogProps = computed(() => {
  const props: { steps?: string[]; expiresAt?: string } = {}
  if (plan.value) props.steps = plan.value.agentPlan.steps
  if (planExpiresAt.value !== undefined) props.expiresAt = planExpiresAt.value
  return props
})

const planIsRestore = computed(() => {
  const current = plan.value
  const resource = planResource.value
  if (!current || !resource) return false
  return resource.type === 'restore-points' && (current.operation.toLowerCase().includes('restore') || current.agentPlan.interruption)
})

const restoreDatabaseName = computed(() => {
  const resource = planResource.value
  if (!resource || resource.type !== 'restore-points') return ''
  const point = restorePoints.value.find((item) => item.id === resource.id)
  return point ? databaseLabel(point.databaseId) : ''
})

const restoreConfirmOpen = ref(false)

function onApprove() {
  if (planIsRestore.value) {
    restoreConfirmOpen.value = true
    return
  }
  void applyCurrentPlan()
}

async function applyCurrentPlan() {
  const resource = planResource.value
  if (!resource) return
  restoreConfirmOpen.value = false
  planDialogOpen.value = false
  await applyRunner.run(async () => (await applyPlan(resource.type, resource.id)).id, {
    onSettled: refreshAll,
    successToast: 'Plan applied',
    failureMessage: 'Applying the plan failed',
  })
}

// --- Row actions ---

const rotateTarget = ref<DatabaseRole>()
const rotatePendingId = ref<string>()
const backupPendingId = ref<string>()
const restorePendingId = ref<string>()

// Clear each pending row marker once its runner settles so a stale id can
// never light up a row when the runner becomes busy again later.
watch(rotateRunner.busy, (busy) => {
  if (!busy) rotatePendingId.value = undefined
})
watch(backupRunner.busy, (busy) => {
  if (!busy) backupPendingId.value = undefined
})
watch(restoreRunner.busy, (busy) => {
  if (!busy) restorePendingId.value = undefined
})

function confirmRotate() {
  const role = rotateTarget.value
  if (!role) return
  rotateTarget.value = undefined
  rotatePendingId.value = role.id
  clearCredential()
  void rotateRunner.run(async () => (await rotateRole(role.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => openPlanReview({ type: 'roles', id: role.id, label: `Rotate ${role.name}` }),
    failureMessage: 'Rotating the credential failed',
  })
}

function backupDatabase(database: ManagedDatabase) {
  backupPendingId.value = database.id
  void backupRunner.run(async () => (await createBackup(database.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Backed up ${database.name}`,
    failureMessage: 'The backup failed',
  })
}

function prepareRestorePlan(point: RestorePoint) {
  restorePendingId.value = point.id
  void restoreRunner.run(async () => (await prepareRestore(point.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => openPlanReview({ type: 'restore-points', id: point.id, label: `Restore ${databaseLabel(point.databaseId)}` }),
    failureMessage: 'Preparing the restore failed',
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
  revealError.value = ''
  revealTarget.value = role
}

function clearCredential() {
  credential.value = ''
  revealedRole.value = undefined
}

async function confirmReveal() {
  const role = revealTarget.value
  if (!role) return
  revealBusy.value = true
  revealError.value = ''
  try {
    credential.value = await revealCredential(role.id)
    revealedRole.value = role
    revealTarget.value = undefined
    await rolesQuery.refetch()
    await nextTick()
    credentialCardEl.value?.scrollIntoView({ block: 'center', behavior: 'smooth' })
    credentialCardEl.value?.focus({ preventScroll: true })
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
    facts.push({
      label: 'Instance',
      value: `PostgreSQL ${instance.version} · ${instance.cluster}`,
      to: selectedLink(instance.id),
    })
    if (instance.port) facts.push({ label: 'Port', value: String(instance.port), mono: true })
    else facts.push({ label: 'Socket', value: instance.socketPath, mono: true })
  }
  const first = owned[0]
  if (first) {
    facts.push({
      label: owned.length === 1 ? 'Owns database' : 'Owns databases',
      value: owned.map((database) => database.name).join(', '),
      ...(owned.length === 1 ? { to: selectedLink(first.id) } : {}),
    })
  }
  return facts
})

// --- URL wiring ---

watch(
  () => route.query.selected,
  (value) => {
    const id = typeof value === 'string' ? value : undefined
    if (id === selectedId.value) return
    selectedId.value = id
    if (!id) return
    planDialogOpen.value = false
    pendingScrollId.value = id
    void tryScrollToPending()
  },
  { immediate: true },
)

// Retry the deep-link scroll once the lists have loaded.
watch([instances, roles, databases, grants, restorePoints], () => {
  void tryScrollToPending()
})

watch(databaseInstance, () => {
  if (ownerRoleId.value && !ownerOptions.value.some((role) => role.id === ownerRoleId.value)) ownerRoleId.value = ''
})

watch(
  () => route.query.create,
  (value) => {
    if (value === '1') openDatabaseDialog()
  },
  { immediate: true },
)
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Managed data layer"
      title="PostgreSQL"
      description="Instances, roles, databases, grants, and restore points. Every change is planned first and runs only after you approve it. Passwords are shown exactly once after creation or rotation."
    >
      <StatusPill tone="accent" label="Versions 16 · 17 · 18" :pulse="false" />
      <AppButton variant="primary" icon="plus" @click="openDatabaseDialog">Create database</AppButton>
    </PageHeader>

    <JobFailureNotice v-if="applyRunner.error.value" v-bind="failureProps(applyRunner)" />
    <JobProgress v-if="applyRunner.progress.value" :event="applyRunner.progress.value" v-bind="progressProps(applyRunner)" />

    <div v-if="credential" ref="credentialCardEl" tabindex="-1" class="rounded-2xl focus:outline-none">
      <CredentialReveal
        :credential="credential"
        :account-label="revealedRole?.name ?? ''"
        :facts="revealFacts"
        @clear="clearCredential"
      />
    </div>

    <!-- Instances -->
    <AppCard eyebrow="Clusters" title="Instances" flush>
      <template #actions>
        <AppButton size="sm" icon="plus" @click="openInstanceDialog">Provision instance</AppButton>
      </template>
      <div class="px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="instancesQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="index in 3" :key="index" />
        </div>
        <AppAlert v-else-if="instancesQuery.isError.value" tone="danger" class="m-2">
          <div class="flex flex-wrap items-center gap-3">
            <span>Instances could not be loaded.</span>
            <AppButton size="sm" @click="instancesQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <EmptyState
          v-else-if="instances.length === 0"
          icon="database"
          title="No instances yet"
          description="Create one to host roles and databases. Each instance gets its own port, data path, and systemd unit."
          class="m-2"
        >
          <template #action>
            <AppButton icon="plus" @click="openInstanceDialog">Provision instance</AppButton>
          </template>
        </EmptyState>
        <div v-else class="space-y-1">
          <ResourceRow
            v-for="item in instances"
            :id="`resource-${item.id}`"
            :key="item.id"
            :title="item.cluster"
            :subtitle="item.dataPath"
            :avatar="item.version"
            clickable
            :selected="selectedId === item.id"
            @select="select(item.id)"
          >
            <template #meta>
              <span class="hidden shrink-0 font-mono text-xs text-ink-muted sm:inline">Port {{ item.port }}</span>
            </template>
            <template #actions>
              <AppButton
                v-if="item.status === 'plan_ready'"
                size="sm"
                :loading="planLoadingId === item.id"
                @click="openPlanReview({ type: 'instances', id: item.id, label: `PostgreSQL ${item.version} · ${item.cluster}` })"
              >
                Review
              </AppButton>
            </template>
            <template #status>
              <StatusPill :status="item.status" />
            </template>
          </ResourceRow>
        </div>
      </div>
    </AppCard>

    <!-- Roles and databases -->
    <div class="grid gap-4 lg:grid-cols-2">
      <AppCard eyebrow="Login identities" title="Roles" flush>
        <template #actions>
          <AppButton size="sm" icon="plus" @click="openRoleDialog">Create role</AppButton>
        </template>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="rotateRunner.error.value || rotateRunner.progress.value" class="mb-2 space-y-2 px-1">
            <JobFailureNotice v-if="rotateRunner.error.value" v-bind="failureProps(rotateRunner)" />
            <JobProgress
              v-if="rotateRunner.progress.value"
              :event="rotateRunner.progress.value"
              v-bind="progressProps(rotateRunner)"
            />
          </div>
          <div v-if="rolesQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="index in 3" :key="index" />
          </div>
          <AppAlert v-else-if="rolesQuery.isError.value" tone="danger" class="m-2">
            <div class="flex flex-wrap items-center gap-3">
              <span>Roles could not be loaded.</span>
              <AppButton size="sm" @click="rolesQuery.refetch()">Retry</AppButton>
            </div>
          </AppAlert>
          <EmptyState
            v-else-if="roles.length === 0"
            icon="key"
            title="No roles yet"
            description="Roles are the login identities that own and access databases. Create one on an active instance."
            class="m-2"
          >
            <template #action>
              <AppButton icon="plus" @click="openRoleDialog">Create role</AppButton>
            </template>
          </EmptyState>
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="role in roles"
              :id="`resource-${role.id}`"
              :key="role.id"
              :title="role.name"
              :subtitle="`${instanceLabel(role.instanceId)} · credential v${role.credentialVersion}`"
              icon="key"
              clickable
              :selected="selectedId === role.id"
              @select="select(role.id)"
            >
              <template #actions>
                <AppButton
                  v-if="role.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === role.id"
                  @click="openPlanReview({ type: 'roles', id: role.id, label: role.name })"
                >
                  Review
                </AppButton>
                <AppButton v-if="role.credentialAvailable" size="sm" icon="key" @click="askReveal(role)">
                  Reveal once
                </AppButton>
                <AppButton
                  v-if="role.status === 'active'"
                  size="sm"
                  icon="refresh-cw"
                  :loading="rotateRunner.busy.value && rotatePendingId === role.id"
                  :disabled="rotateRunner.busy.value"
                  @click="rotateTarget = role"
                >
                  Rotate
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="role.status" />
              </template>
            </ResourceRow>
          </div>
        </div>
      </AppCard>

      <AppCard eyebrow="Owned resources" title="Databases" flush>
        <template #actions>
          <AppButton size="sm" icon="plus" @click="openDatabaseDialog">Create database</AppButton>
        </template>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="backupRunner.error.value || backupRunner.progress.value" class="mb-2 space-y-2 px-1">
            <JobFailureNotice v-if="backupRunner.error.value" v-bind="failureProps(backupRunner)" />
            <JobProgress
              v-if="backupRunner.progress.value"
              :event="backupRunner.progress.value"
              v-bind="progressProps(backupRunner)"
            />
          </div>
          <div v-if="databasesQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="index in 3" :key="index" />
          </div>
          <AppAlert v-else-if="databasesQuery.isError.value" tone="danger" class="m-2">
            <div class="flex flex-wrap items-center gap-3">
              <span>Databases could not be loaded.</span>
              <AppButton size="sm" @click="databasesQuery.refetch()">Retry</AppButton>
            </div>
          </AppAlert>
          <EmptyState
            v-else-if="databases.length === 0"
            icon="database"
            title="No databases yet"
            description="Create a database on an active instance, owned by a role."
            class="m-2"
          >
            <template #action>
              <AppButton icon="plus" @click="openDatabaseDialog">Create database</AppButton>
            </template>
          </EmptyState>
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="item in databases"
              :id="`resource-${item.id}`"
              :key="item.id"
              :title="item.name"
              :subtitle="`Owner ${roleLabel(item.ownerRoleId)}`"
              icon="database"
              clickable
              :selected="selectedId === item.id"
              @select="select(item.id)"
            >
              <template #actions>
                <AppButton
                  v-if="item.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === item.id"
                  @click="openPlanReview({ type: 'databases', id: item.id, label: item.name })"
                >
                  Review
                </AppButton>
                <AppButton
                  v-if="item.status === 'active'"
                  size="sm"
                  icon="copy"
                  :loading="backupRunner.busy.value && backupPendingId === item.id"
                  :disabled="backupRunner.busy.value"
                  @click="backupDatabase(item)"
                >
                  Back up
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="item.status" />
              </template>
            </ResourceRow>
          </div>
        </div>
      </AppCard>
    </div>

    <!-- Grants and restore points -->
    <div class="grid gap-4 lg:grid-cols-2">
      <AppCard eyebrow="Scoped permissions" title="Grants" flush>
        <template #actions>
          <AppButton size="sm" icon="plus" @click="openGrantDialog">Grant access</AppButton>
        </template>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="grantsQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="index in 3" :key="index" />
          </div>
          <AppAlert v-else-if="grantsQuery.isError.value" tone="danger" class="m-2">
            <div class="flex flex-wrap items-center gap-3">
              <span>Grants could not be loaded.</span>
              <AppButton size="sm" @click="grantsQuery.refetch()">Retry</AppButton>
            </div>
          </AppAlert>
          <EmptyState
            v-else-if="grants.length === 0"
            icon="shield"
            title="No grants yet"
            description="Give a role scoped access to a database — connect only, read only, or read and write."
            class="m-2"
          >
            <template #action>
              <AppButton icon="plus" @click="openGrantDialog">Grant access</AppButton>
            </template>
          </EmptyState>
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="grant in grants"
              :id="`resource-${grant.id}`"
              :key="grant.id"
              :title="`${roleLabel(grant.roleId)} → ${databaseLabel(grant.databaseId)}`"
              :subtitle="ACCESS_LABELS[grant.access]"
              icon="shield"
              clickable
              :selected="selectedId === grant.id"
              @select="select(grant.id)"
            >
              <template #actions>
                <AppButton
                  v-if="grant.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === grant.id"
                  @click="openPlanReview({ type: 'grants', id: grant.id, label: `${roleLabel(grant.roleId)} → ${databaseLabel(grant.databaseId)}` })"
                >
                  Review
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="grant.status" />
              </template>
            </ResourceRow>
          </div>
        </div>
      </AppCard>

      <AppCard eyebrow="Recovery" title="Restore points" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="restoreRunner.error.value || restoreRunner.progress.value" class="mb-2 space-y-2 px-1">
            <JobFailureNotice v-if="restoreRunner.error.value" v-bind="failureProps(restoreRunner)" />
            <JobProgress
              v-if="restoreRunner.progress.value"
              :event="restoreRunner.progress.value"
              v-bind="progressProps(restoreRunner)"
            />
          </div>
          <div v-if="restorePointsQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="index in 3" :key="index" />
          </div>
          <AppAlert v-else-if="restorePointsQuery.isError.value" tone="danger" class="m-2">
            <div class="flex flex-wrap items-center gap-3">
              <span>Restore points could not be loaded.</span>
              <AppButton size="sm" @click="restorePointsQuery.refetch()">Retry</AppButton>
            </div>
          </AppAlert>
          <EmptyState
            v-else-if="restorePoints.length === 0"
            icon="rotate-ccw"
            title="No restore points yet"
            description="Back up an active database to create a verified restore point."
            class="m-2"
          />
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="point in restorePoints"
              :id="`resource-${point.id}`"
              :key="point.id"
              :title="databaseLabel(point.databaseId)"
              :subtitle="`${point.verifiedAt ? formatDateTime(point.verifiedAt) : 'Verification pending'} · ${formatBytes(point.sizeBytes)}`"
              icon="rotate-ccw"
              clickable
              :selected="selectedId === point.id"
              @select="select(point.id)"
            >
              <template #actions>
                <AppButton
                  v-if="point.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === point.id"
                  @click="openPlanReview({ type: 'restore-points', id: point.id, label: `Restore ${databaseLabel(point.databaseId)}` })"
                >
                  Review
                </AppButton>
                <AppButton
                  v-if="point.status === 'verified'"
                  size="sm"
                  variant="danger"
                  :loading="restoreRunner.busy.value && restorePendingId === point.id"
                  :disabled="restoreRunner.busy.value"
                  @click="prepareRestorePlan(point)"
                >
                  Prepare restore
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="point.status" />
              </template>
            </ResourceRow>
          </div>
        </div>
      </AppCard>
    </div>

    <!-- Create dialogs -->
    <AppDialog :open="showInstanceDialog" title="Provision instance" @close="showInstanceDialog = false">
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

    <AppDialog :open="showRoleDialog" title="Create role" @close="showRoleDialog = false">
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

    <AppDialog :open="showDatabaseDialog" title="Create database" @close="closeDatabaseDialog">
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

    <AppDialog :open="showGrantDialog" title="Grant access" @close="showGrantDialog = false">
      <form class="space-y-4" novalidate @submit.prevent="submitGrant">
        <FormField label="Database" :error="grantDatabaseError">
          <AppSelect
            v-model="grantDatabaseId"
            :invalid="!!grantDatabaseError"
            empty-message="No active databases — create one first"
          >
            <option v-if="activeDatabases.length" disabled value="">Select database</option>
            <option v-for="item in activeDatabases" :key="item.id" :value="item.id">{{ item.name }}</option>
          </AppSelect>
        </FormField>
        <FormField label="Role" :error="grantRoleError">
          <AppSelect v-model="grantRoleId" :invalid="!!grantRoleError" empty-message="No active roles — create one first">
            <option v-if="activeRoles.length" disabled value="">Select role</option>
            <option v-for="role in activeRoles" :key="role.id" :value="role.id">{{ role.name }}</option>
          </AppSelect>
        </FormField>
        <FormField label="Access">
          <AppSelect v-model="access">
            <option value="connect">Connect only</option>
            <option value="read_only">Read only</option>
            <option value="read_write">Read and write</option>
          </AppSelect>
        </FormField>
        <JobFailureNotice v-if="grantRunner.error.value" v-bind="failureProps(grantRunner)" />
        <JobProgress
          v-if="grantRunner.progress.value"
          :event="grantRunner.progress.value"
          v-bind="progressProps(grantRunner)"
        />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <AppButton :disabled="grantRunner.busy.value" @click="showGrantDialog = false">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="grantRunner.busy.value">Grant access</AppButton>
        </div>
      </form>
    </AppDialog>

    <!-- Plan review -->
    <PlanReviewDialog
      :open="planDialogOpen"
      :title="planResource?.label ?? 'Review plan'"
      :facts="planFacts"
      :warnings="planWarnings"
      :busy="planBusy || applyRunner.busy.value"
      approve-label="Approve and execute"
      v-bind="planDialogProps"
      @approve="onApprove"
      @regenerate="regeneratePlan"
      @close="planDialogOpen = false"
    />

    <!-- Confirmation gates -->
    <AppConfirmDialog
      :open="restoreConfirmOpen"
      :title="restoreDatabaseName ? `Restore ${restoreDatabaseName}?` : 'Restore database?'"
      confirm-label="Restore database"
      :type-to-confirm="restoreDatabaseName"
      @confirm="applyCurrentPlan"
      @close="restoreConfirmOpen = false"
    >
      Restoring replaces the current data and terminates active connections. Anything written since this backup is lost.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="!!rotateTarget"
      :title="rotateTarget ? `Rotate credential for ${rotateTarget.name}?` : 'Rotate credential?'"
      confirm-label="Rotate credential"
      @confirm="confirmRotate"
      @close="rotateTarget = undefined"
    >
      Connected applications will fail until the new password is deployed. You review a plan before anything changes, and
      the new password is shown exactly once.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="!!revealTarget"
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
