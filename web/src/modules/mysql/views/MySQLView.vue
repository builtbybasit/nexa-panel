<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatBytes, formatDateTime, humanize } from '@/shared/formatters'
import { useJobRunner, type FollowOptions } from '@/shared/composables/useJobRunner'
import { useToasts } from '@/shared/composables/useToasts'
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
  FactList,
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
  createAccount,
  createBackup,
  createDatabase,
  createGrant,
  getPlan,
  listAccounts,
  listDatabases,
  listEngines,
  listGrants,
  listRestorePoints,
  prepareRestore,
  revealCredential,
  rotateAccount,
  type Access,
  type Account,
  type Plan,
  type ResourceType,
  type RestorePoint,
} from '../api'

interface SelectedResource {
  type: ResourceType
  id: string
  label: string
}

const route = useRoute()
const router = useRouter()

const enginesQuery = useQuery({ queryKey: ['mysql-engines'], queryFn: listEngines, retry: false })
const accountsQuery = useQuery({ queryKey: ['mysql-accounts'], queryFn: listAccounts, retry: false })
const databasesQuery = useQuery({ queryKey: ['mysql-databases'], queryFn: listDatabases, retry: false })
const grantsQuery = useQuery({ queryKey: ['mysql-grants'], queryFn: listGrants, retry: false })
const restorePointsQuery = useQuery({ queryKey: ['mysql-restore-points'], queryFn: listRestorePoints, retry: false })

const engines = computed(() => enginesQuery.data.value ?? [])
const engine = computed(() => engines.value[0])
const accounts = computed(() => accountsQuery.data.value ?? [])
const databases = computed(() => databasesQuery.data.value ?? [])
const grants = computed(() => grantsQuery.data.value ?? [])
const restorePoints = computed(() => restorePointsQuery.data.value ?? [])
const activeAccounts = computed(() => accounts.value.filter((item) => item.status === 'active'))
const activeDatabases = computed(() => databases.value.filter((item) => item.status === 'active'))

const accessLabels: Record<Access, string> = {
  connect: 'Connect only',
  read_only: 'Read only',
  read_write: 'Read and write',
}

const namePattern = /^[a-z][a-z0-9_]+$/
const nameError = 'Use lowercase letters, digits and underscores, starting with a letter.'

// Dialog state
const createAccountOpen = ref(false)
const createDatabaseOpen = ref(false)
const grantOpen = ref(false)
const rotateTarget = ref<Account>()
const revealTarget = ref<Account>()
const restoreTarget = ref<RestorePoint>()
const planDialog = ref<{ resource: SelectedResource; plan: Plan }>()
const planRefreshing = ref(false)
const planLoadingId = ref<string>()
/** Set when approving a restore plan; applying waits for a type-to-confirm. */
const applyRestoreConfirmOpen = ref(false)

// Form state
const accountName = ref('')
const accountHost = ref('localhost')
const accountNameError = ref('')
const databaseName = ref('')
const databaseNameError = ref('')
const ownerAccountId = ref('')
const grantDatabaseId = ref('')
const grantAccountId = ref('')
const access = ref<Access>('read_write')
const dialogError = ref('')

const credential = ref('')
const credentialFor = ref<{ label: string; facts: Fact[] }>()
const revealBusy = ref(false)
const pageError = ref('')

const runner = useJobRunner()
const toasts = useToasts()

const BUSY_MESSAGE = 'Another operation is still running. Wait for it to finish, then try again.'

// Per-operation busy scoping: one runner, but each control only spins for its
// own operation while everything else just disables.
const pendingAction = ref<string>()
watch(runner.busy, (busy) => {
  if (!busy) pendingAction.value = undefined
})
const isBusy = (key: string) => runner.busy.value && pendingAction.value === key

const failedJobId = computed(() =>
  runner.progress.value && runner.jobId.value !== undefined ? runner.jobId.value : undefined,
)

const relativeFormat = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

function relativeTime(timestamp: string): string {
  const deltaMs = new Date(timestamp).getTime() - Date.now()
  const units = [
    ['day', 86_400_000],
    ['hour', 3_600_000],
    ['minute', 60_000],
  ] as const
  for (const [unit, size] of units) {
    if (Math.abs(deltaMs) >= size) return relativeFormat.format(Math.round(deltaMs / size), unit)
  }
  return 'just now'
}

function verifiedText(timestamp?: string) {
  return timestamp ? `${formatDateTime(timestamp)} (${relativeTime(timestamp)})` : 'Not verified yet'
}

function databaseLabel(id: string) {
  return databases.value.find((database) => database.id === id)?.name ?? id
}

function accountLabel(id: string) {
  const account = accounts.value.find((item) => item.id === id)
  return account ? `${account.name}@${account.host}` : id
}

// URL selection (?selected=<id>): restore on mount, highlight, scroll into view.
const selectedId = computed(() => (typeof route.query.selected === 'string' ? route.query.selected : undefined))

function select(id: string) {
  if (selectedId.value !== id) void router.replace({ query: { ...route.query, selected: id } })
}

function toggleSelected(id: string) {
  if (selectedId.value === id) {
    const query = { ...route.query }
    delete query.selected
    void router.replace({ query })
  } else {
    select(id)
  }
}

let selectionRestored = false
watch(
  [accounts, databases, grants, restorePoints],
  async () => {
    if (selectionRestored) return
    const id = selectedId.value
    if (!id) {
      selectionRestored = true
      return
    }
    await nextTick()
    const row = document.querySelector(`[data-resource-id="${CSS.escape(id)}"]`)
    if (row) {
      row.scrollIntoView({ block: 'center' })
      selectionRestored = true
    }
  },
  { immediate: true },
)

function failureDetail(
  items: { id: string; failure?: string; lastJobId?: number }[],
): { message: string; jobId?: number } | undefined {
  const item = items.find((entry) => entry.id === selectedId.value)
  if (!item?.failure) return undefined
  const detail: { message: string; jobId?: number } = { message: item.failure }
  if (item.lastJobId !== undefined) detail.jobId = item.lastJobId
  return detail
}

const accountFailure = computed(() => failureDetail(accounts.value))
const databaseFailure = computed(() => failureDetail(databases.value))
const grantFailure = computed(() => failureDetail(grants.value))
const restorePointFailure = computed(() => failureDetail(restorePoints.value))

async function refreshAll() {
  await Promise.all([
    enginesQuery.refetch(),
    accountsQuery.refetch(),
    databasesQuery.refetch(),
    grantsQuery.refetch(),
    restorePointsQuery.refetch(),
  ])
}

/** Queues an operation; resolves to undefined on success or a reason the caller can show. */
async function queue(
  key: string,
  action: () => Promise<{ jobId: number; resource: SelectedResource }>,
  opts: { failureMessage: string; successToast?: string; openPlanAfter?: boolean },
): Promise<string | undefined> {
  if (runner.busy.value) return BUSY_MESSAGE
  pendingAction.value = key
  let resource: SelectedResource | undefined
  const options: FollowOptions = {
    onSettled: refreshAll,
    onSuccess: async () => {
      if (!resource) return
      select(resource.id)
      if (opts.openPlanAfter) await openPlan(resource)
    },
    failureMessage: opts.failureMessage,
  }
  if (opts.successToast) options.successToast = opts.successToast
  await runner.run(async () => {
    const result = await action()
    resource = result.resource
    return result.jobId
  }, options)
  return runner.error.value || undefined
}

// Plan review
async function openPlan(resource: SelectedResource) {
  planRefreshing.value = true
  try {
    const plan = (await getPlan(resource.type, resource.id)).plan
    planDialog.value = { resource, plan }
  } catch (caught) {
    planDialog.value = undefined
    // A toast instead of the top-of-page error: the Review buttons live far
    // down the page and would otherwise appear to do nothing.
    toasts.push({
      title: 'Could not load the plan',
      body: caught instanceof Error ? caught.message : 'The plan is not ready yet.',
      tone: 'danger',
    })
  } finally {
    planRefreshing.value = false
  }
}

async function reviewPlan(resource: SelectedResource) {
  select(resource.id)
  planLoadingId.value = resource.id
  try {
    await openPlan(resource)
  } finally {
    planLoadingId.value = undefined
  }
}

async function regeneratePlan() {
  const current = planDialog.value
  if (current) await openPlan(current.resource)
}

const planFacts = computed<Fact[]>(() => {
  const current = planDialog.value
  if (!current) return []
  const { resource, plan } = current
  const facts: Fact[] = [{ label: 'Operation', value: humanize(plan.operation) }]
  if (resource.type === 'accounts') {
    const account = accounts.value.find((item) => item.id === resource.id)
    if (account) facts.push({ label: 'Account', value: `${account.name}@${account.host}`, mono: true })
  } else if (resource.type === 'databases') {
    const database = databases.value.find((item) => item.id === resource.id)
    if (database) {
      facts.push(
        { label: 'Database', value: database.name, mono: true },
        { label: 'Owner', value: accountLabel(database.ownerAccountId), mono: true },
      )
    }
  } else if (resource.type === 'grants') {
    const grant = grants.value.find((item) => item.id === resource.id)
    if (grant) {
      facts.push(
        { label: 'Database', value: databaseLabel(grant.databaseId), mono: true },
        { label: 'Account', value: accountLabel(grant.accountId), mono: true },
        { label: 'Access', value: accessLabels[grant.access] },
      )
    }
  } else {
    const point = restorePoints.value.find((item) => item.id === resource.id)
    if (point) {
      facts.push(
        { label: 'Database', value: databaseLabel(point.databaseId), mono: true },
        { label: 'Verified', value: verifiedText(point.verifiedAt) },
        { label: 'Size', value: formatBytes(point.sizeBytes) },
      )
      if (point.sha256) facts.push({ label: 'Checksum', value: point.sha256, mono: true })
    }
  }
  const activeEngine = engine.value
  if (activeEngine) facts.push({ label: 'Engine', value: `${activeEngine.kind} ${activeEngine.version}` })
  return facts
})

const planDialogProps = computed(() => {
  const current = planDialog.value
  if (!current) return { open: false, title: '' }
  return {
    open: true,
    title: `Review plan · ${current.resource.label}`,
    steps: current.plan.agentPlan.steps,
    facts: planFacts.value,
    warnings: current.plan.agentPlan.warnings,
    expiresAt: current.plan.expiresAt,
  }
})

// Applying a restore replaces data; match the PostgreSQL page and require a
// type-to-confirm at apply time, not only when the plan was prepared.
const planIsRestore = computed(() => {
  const current = planDialog.value
  if (!current) return false
  return (
    current.resource.type === 'restore-points' &&
    (current.plan.operation.toLowerCase().includes('restore') || current.plan.agentPlan.interruption)
  )
})

const restorePlanDatabaseName = computed(() => {
  const current = planDialog.value
  if (!current || current.resource.type !== 'restore-points') return ''
  const point = restorePoints.value.find((item) => item.id === current.resource.id)
  return point ? databaseLabel(point.databaseId) : ''
})

function approvePlan() {
  if (!planDialog.value || runner.busy.value) return
  if (planIsRestore.value) {
    applyRestoreConfirmOpen.value = true
    return
  }
  void applyCurrentPlan()
}

async function applyCurrentPlan() {
  const current = planDialog.value
  if (!current || runner.busy.value) return
  applyRestoreConfirmOpen.value = false
  pendingAction.value = 'apply'
  await runner.run(async () => (await applyPlan(current.resource.type, current.resource.id)).id, {
    onSettled: async () => {
      planDialog.value = undefined
      await refreshAll()
    },
    failureMessage: 'The plan could not be applied',
    successToast: 'Plan applied',
  })
  if (runner.error.value) planDialog.value = undefined
}

// Create account
function openCreateAccount() {
  accountName.value = ''
  accountHost.value = 'localhost'
  accountNameError.value = ''
  dialogError.value = ''
  createAccountOpen.value = true
}

watch(accountName, () => (accountNameError.value = ''))

async function submitCreateAccount() {
  if (!namePattern.test(accountName.value)) {
    accountNameError.value = nameError
    return
  }
  const activeEngine = engine.value
  if (!activeEngine) return
  dialogError.value = ''
  const failure = await queue(
    'create-account',
    async () => {
      const result = await createAccount({
        engineId: activeEngine.id,
        name: accountName.value,
        host: accountHost.value,
      })
      return {
        jobId: result.job.id,
        resource: { type: 'accounts', id: result.account.id, label: `${result.account.name}@${result.account.host}` },
      }
    },
    { failureMessage: 'Account creation failed', openPlanAfter: true },
  )
  if (failure) dialogError.value = failure
  else createAccountOpen.value = false
}

// Create database (?create=1 deep link)
function openCreateDatabase() {
  databaseName.value = ''
  databaseNameError.value = ''
  ownerAccountId.value = ''
  dialogError.value = ''
  createDatabaseOpen.value = true
  if (route.query.create !== '1') void router.replace({ query: { ...route.query, create: '1' } })
}

function closeCreateDatabase() {
  createDatabaseOpen.value = false
  if ('create' in route.query) {
    const query = { ...route.query }
    delete query.create
    void router.replace({ query })
  }
}

watch(databaseName, () => (databaseNameError.value = ''))

async function submitCreateDatabase() {
  if (!namePattern.test(databaseName.value)) {
    databaseNameError.value = nameError
    return
  }
  const activeEngine = engine.value
  if (!activeEngine) return
  dialogError.value = ''
  const failure = await queue(
    'create-database',
    async () => {
      const result = await createDatabase({
        engineId: activeEngine.id,
        name: databaseName.value,
        ownerAccountId: ownerAccountId.value,
      })
      return {
        jobId: result.job.id,
        resource: { type: 'databases', id: result.database.id, label: result.database.name },
      }
    },
    { failureMessage: 'Database creation failed', openPlanAfter: true },
  )
  if (failure) dialogError.value = failure
  else closeCreateDatabase()
}

// Grant access
function openGrant() {
  grantDatabaseId.value = ''
  grantAccountId.value = ''
  access.value = 'read_write'
  dialogError.value = ''
  grantOpen.value = true
}

async function submitGrant() {
  dialogError.value = ''
  const failure = await queue(
    'create-grant',
    async () => {
      const result = await createGrant({
        databaseId: grantDatabaseId.value,
        accountId: grantAccountId.value,
        access: access.value,
      })
      return {
        jobId: result.job.id,
        resource: {
          type: 'grants',
          id: result.grant.id,
          label: `${databaseLabel(grantDatabaseId.value)} → ${accountLabel(grantAccountId.value)}`,
        },
      }
    },
    { failureMessage: 'Access grant failed', openPlanAfter: true },
  )
  if (failure) dialogError.value = failure
  else grantOpen.value = false
}

// Backup
function backup(databaseId: string, name: string) {
  void queue(
    `backup:${databaseId}`,
    async () => {
      const result = await createBackup(databaseId)
      return { jobId: result.job.id, resource: { type: 'restore-points', id: result.restorePoint.id, label: name } }
    },
    { failureMessage: 'Backup failed', successToast: 'Backup created' },
  )
}

// Restore (type-to-confirm)
const restoreFacts = computed<Fact[]>(() => {
  const point = restoreTarget.value
  if (!point) return []
  return [
    { label: 'Database', value: databaseLabel(point.databaseId), mono: true },
    { label: 'Verified', value: verifiedText(point.verifiedAt) },
    { label: 'Size', value: formatBytes(point.sizeBytes) },
  ]
})

function confirmRestore() {
  const point = restoreTarget.value
  if (!point) return
  restoreTarget.value = undefined
  void queue(
    `restore:${point.id}`,
    async () => {
      const result = await prepareRestore(point.id)
      return {
        jobId: result.job.id,
        resource: {
          type: 'restore-points',
          id: result.restorePoint.id,
          label: `Restore ${databaseLabel(point.databaseId)}`,
        },
      }
    },
    { failureMessage: 'Restore preparation failed', openPlanAfter: true },
  )
}

// Rotate (confirm first — the old password stops working once the plan applies)
function confirmRotate() {
  const account = rotateTarget.value
  if (!account) return
  rotateTarget.value = undefined
  credential.value = ''
  credentialFor.value = undefined
  void queue(
    `rotate:${account.id}`,
    async () => {
      const result = await rotateAccount(account.id)
      return {
        jobId: result.job.id,
        resource: { type: 'accounts', id: account.id, label: `${account.name}@${account.host}` },
      }
    },
    { failureMessage: 'Password rotation failed', openPlanAfter: true },
  )
}

// Reveal (confirm first — the credential is shown exactly once)
function credentialFacts(account: Account): Fact[] {
  const facts: Fact[] = [
    { label: 'Account', value: account.name, mono: true },
    { label: 'Host', value: account.host, mono: true },
  ]
  const activeEngine = engine.value
  if (activeEngine) {
    facts.push(
      { label: 'Engine', value: `${activeEngine.kind} ${activeEngine.version}` },
      { label: 'Port', value: String(activeEngine.port) },
    )
  }
  return facts
}

async function confirmReveal() {
  const account = revealTarget.value
  if (!account) return
  revealBusy.value = true
  pageError.value = ''
  try {
    credential.value = await revealCredential(account.id)
    credentialFor.value = { label: `${account.name}@${account.host}`, facts: credentialFacts(account) }
    select(account.id)
    await accountsQuery.refetch()
  } catch (caught) {
    pageError.value = caught instanceof Error ? caught.message : 'The password could not be revealed.'
  } finally {
    revealBusy.value = false
    revealTarget.value = undefined
  }
}

function clearCredential() {
  credential.value = ''
  credentialFor.value = undefined
}

onMounted(() => {
  if (route.query.create === '1') openCreateDatabase()
})
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="One native engine"
      title="MySQL & MariaDB"
      description="Nexa manages the one active MySQL or MariaDB server on this host. Every change is planned first and only happens after you approve it."
    >
      <StatusPill
        v-if="engine"
        tone="success"
        :label="`${engine.kind} ${engine.version}`"
        description="Discovered through its local Unix socket"
        :pulse="false"
      />
    </PageHeader>

    <AppAlert v-if="enginesQuery.isError.value" tone="danger">
      <p>The MySQL and MariaDB engines could not be loaded.</p>
      <AppButton size="sm" class="mt-2" @click="enginesQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <AppAlert v-if="enginesQuery.isSuccess.value && !engine" tone="warning">
      No active native MySQL or MariaDB server was discovered through its local Unix socket.
    </AppAlert>
    <JobFailureNotice
      v-if="runner.error.value"
      :message="runner.error.value"
      v-bind="failedJobId !== undefined ? { jobId: failedJobId } : {}"
    />
    <AppAlert v-if="pageError" tone="danger">{{ pageError }}</AppAlert>
    <JobProgress
      v-if="runner.progress.value"
      :event="runner.progress.value"
      :messages="runner.messages.value"
      v-bind="runner.startedAtMs.value !== undefined ? { startedAtMs: runner.startedAtMs.value } : {}"
    />
    <CredentialReveal
      v-if="credential && credentialFor"
      :credential="credential"
      :account-label="credentialFor.label"
      :facts="credentialFor.facts"
      @clear="clearCredential"
    />

    <!-- Accounts and databases -->
    <div class="grid gap-4 lg:grid-cols-2">
      <AppCard eyebrow="Login identities" title="Accounts" flush>
        <template #actions>
          <AppButton size="sm" icon="plus" :disabled="!engine" @click="openCreateAccount">Create account</AppButton>
        </template>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="accountsQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="n in 3" :key="n" />
          </div>
          <AppAlert v-else-if="accountsQuery.isError.value" tone="danger" class="m-2">
            <p>Accounts could not be loaded.</p>
            <AppButton size="sm" class="mt-2" @click="accountsQuery.refetch()">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="accounts.length === 0"
            icon="key"
            title="No accounts yet"
            description="Accounts are the logins apps use to connect. Create one, then give it access to a database."
            class="m-2"
          >
            <template #action>
              <AppButton size="sm" icon="plus" :disabled="!engine" @click="openCreateAccount">Create account</AppButton>
            </template>
          </EmptyState>
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="item in accounts"
              :key="item.id"
              :data-resource-id="item.id"
              :title="`${item.name}@${item.host}`"
              :subtitle="`Password v${item.credentialVersion}`"
              icon="key"
              clickable
              :selected="selectedId === item.id"
              @select="toggleSelected(item.id)"
            >
              <template #actions>
                <AppButton
                  v-if="item.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === item.id"
                  @click="reviewPlan({ type: 'accounts', id: item.id, label: `${item.name}@${item.host}` })"
                >
                  Review
                </AppButton>
                <AppButton
                  v-if="item.credentialAvailable"
                  size="sm"
                  icon="eye"
                  :disabled="revealBusy"
                  @click="revealTarget = item"
                >
                  Reveal once
                </AppButton>
                <AppButton
                  v-if="item.status === 'active'"
                  size="sm"
                  icon="refresh-cw"
                  :loading="isBusy(`rotate:${item.id}`)"
                  :disabled="runner.busy.value"
                  @click="rotateTarget = item"
                >
                  Rotate
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="item.status" />
              </template>
            </ResourceRow>
            <JobFailureNotice v-if="accountFailure" class="mt-2" v-bind="accountFailure" />
          </div>
        </div>
      </AppCard>

      <AppCard eyebrow="Owned resources" title="Databases" flush>
        <template #actions>
          <AppButton size="sm" icon="plus" :disabled="!engine" @click="openCreateDatabase">Create database</AppButton>
        </template>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="databasesQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="n in 3" :key="n" />
          </div>
          <AppAlert v-else-if="databasesQuery.isError.value" tone="danger" class="m-2">
            <p>Databases could not be loaded.</p>
            <AppButton size="sm" class="mt-2" @click="databasesQuery.refetch()">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="databases.length === 0"
            icon="database"
            title="No databases yet"
            description="Each database is owned by one account. Create an account first, then create the database it owns."
            class="m-2"
          >
            <template #action>
              <AppButton size="sm" icon="plus" :disabled="!engine" @click="openCreateDatabase">
                Create database
              </AppButton>
            </template>
          </EmptyState>
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="item in databases"
              :key="item.id"
              :data-resource-id="item.id"
              :title="item.name"
              :subtitle="`Owned by ${accountLabel(item.ownerAccountId)}`"
              icon="database"
              clickable
              :selected="selectedId === item.id"
              @select="toggleSelected(item.id)"
            >
              <template #actions>
                <AppButton
                  v-if="item.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === item.id"
                  @click="reviewPlan({ type: 'databases', id: item.id, label: item.name })"
                >
                  Review
                </AppButton>
                <AppButton
                  v-if="item.status === 'active'"
                  size="sm"
                  icon="archive"
                  :loading="isBusy(`backup:${item.id}`)"
                  :disabled="runner.busy.value"
                  @click="backup(item.id, item.name)"
                >
                  Back up
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="item.status" />
              </template>
            </ResourceRow>
            <JobFailureNotice v-if="databaseFailure" class="mt-2" v-bind="databaseFailure" />
          </div>
        </div>
      </AppCard>
    </div>

    <!-- Grants and restore points -->
    <div class="grid gap-4 lg:grid-cols-2">
      <AppCard eyebrow="Scoped permissions" title="Access grants" flush>
        <template #actions>
          <AppButton size="sm" icon="plus" @click="openGrant">Grant access</AppButton>
        </template>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="grantsQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="n in 3" :key="n" />
          </div>
          <AppAlert v-else-if="grantsQuery.isError.value" tone="danger" class="m-2">
            <p>Access grants could not be loaded.</p>
            <AppButton size="sm" class="mt-2" @click="grantsQuery.refetch()">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="grants.length === 0"
            icon="shield"
            title="No access grants yet"
            description="Grants let an account read or write a specific database, beyond the one it owns."
            class="m-2"
          >
            <template #action>
              <AppButton size="sm" icon="plus" @click="openGrant">Grant access</AppButton>
            </template>
          </EmptyState>
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="item in grants"
              :key="item.id"
              :data-resource-id="item.id"
              :title="`${databaseLabel(item.databaseId)} → ${accountLabel(item.accountId)}`"
              :subtitle="accessLabels[item.access]"
              icon="shield"
              clickable
              :selected="selectedId === item.id"
              @select="toggleSelected(item.id)"
            >
              <template #actions>
                <AppButton
                  v-if="item.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === item.id"
                  @click="
                    reviewPlan({
                      type: 'grants',
                      id: item.id,
                      label: `${databaseLabel(item.databaseId)} → ${accountLabel(item.accountId)}`,
                    })
                  "
                >
                  Review
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="item.status" />
              </template>
            </ResourceRow>
            <JobFailureNotice v-if="grantFailure" class="mt-2" v-bind="grantFailure" />
          </div>
        </div>
      </AppCard>

      <AppCard eyebrow="Recovery" title="Restore points" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="restorePointsQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="n in 3" :key="n" />
          </div>
          <AppAlert v-else-if="restorePointsQuery.isError.value" tone="danger" class="m-2">
            <p>Restore points could not be loaded.</p>
            <AppButton size="sm" class="mt-2" @click="restorePointsQuery.refetch()">Retry</AppButton>
          </AppAlert>
          <EmptyState
            v-else-if="restorePoints.length === 0"
            icon="rotate-ccw"
            title="No restore points yet"
            description="Back up an active database to create a verified restore point you can roll back to later."
            class="m-2"
          />
          <div v-else class="space-y-1">
            <ResourceRow
              v-for="point in restorePoints"
              :key="point.id"
              :data-resource-id="point.id"
              :title="databaseLabel(point.databaseId)"
              :subtitle="
                point.verifiedAt
                  ? `Verified ${formatDateTime(point.verifiedAt)} · ${relativeTime(point.verifiedAt)} · ${formatBytes(point.sizeBytes)}`
                  : `Not verified yet · ${formatBytes(point.sizeBytes)}`
              "
              icon="rotate-ccw"
              clickable
              :selected="selectedId === point.id"
              @select="toggleSelected(point.id)"
            >
              <template #actions>
                <AppButton
                  v-if="point.status === 'plan_ready'"
                  size="sm"
                  :loading="planLoadingId === point.id"
                  @click="
                    reviewPlan({
                      type: 'restore-points',
                      id: point.id,
                      label: `Restore ${databaseLabel(point.databaseId)}`,
                    })
                  "
                >
                  Review
                </AppButton>
                <AppButton
                  v-if="point.status === 'verified'"
                  size="sm"
                  variant="danger"
                  :loading="isBusy(`restore:${point.id}`)"
                  :disabled="runner.busy.value"
                  @click="restoreTarget = point"
                >
                  Restore
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="point.status" />
              </template>
            </ResourceRow>
            <JobFailureNotice v-if="restorePointFailure" class="mt-2" v-bind="restorePointFailure" />
          </div>
        </div>
      </AppCard>
    </div>

    <!-- Create account dialog -->
    <AppDialog :open="createAccountOpen" title="Create account" @close="createAccountOpen = false">
      <form class="space-y-4" @submit.prevent="submitCreateAccount">
        <p class="text-[13px] leading-relaxed text-ink-secondary">
          You'll review a plan before the account is created.
        </p>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <FormField
          label="Name"
          :error="accountNameError"
          hint="Lowercase letters, digits and underscores, starting with a letter."
        >
          <AppInput
            v-model="accountName"
            :invalid="!!accountNameError"
            required
            autocomplete="off"
            spellcheck="false"
          />
        </FormField>
        <FormField label="Host" hint="Keep localhost for apps on this server, or use % to allow any host.">
          <AppInput v-model="accountHost" required autocomplete="off" spellcheck="false" />
        </FormField>
        <div class="flex justify-end gap-2 pt-1">
          <AppButton :disabled="isBusy('create-account')" @click="createAccountOpen = false">Cancel</AppButton>
          <AppButton
            variant="primary"
            type="submit"
            :loading="isBusy('create-account')"
            :disabled="runner.busy.value || !engine"
          >
            Create account
          </AppButton>
        </div>
      </form>
    </AppDialog>

    <!-- Create database dialog (?create=1) -->
    <AppDialog :open="createDatabaseOpen" title="Create database" @close="closeCreateDatabase">
      <form class="space-y-4" @submit.prevent="submitCreateDatabase">
        <p class="text-[13px] leading-relaxed text-ink-secondary">
          You'll review a plan before the database is created.
        </p>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <FormField
          label="Name"
          :error="databaseNameError"
          hint="Lowercase letters, digits and underscores, starting with a letter."
        >
          <AppInput
            v-model="databaseName"
            :invalid="!!databaseNameError"
            required
            autocomplete="off"
            spellcheck="false"
          />
        </FormField>
        <FormField label="Owner account" hint="The account that owns this database and gets full access to it.">
          <AppSelect v-model="ownerAccountId" required empty-message="No active accounts — create an account first">
            <template v-if="activeAccounts.length">
              <option disabled value="">Select account</option>
              <option v-for="item in activeAccounts" :key="item.id" :value="item.id">
                {{ item.name }}@{{ item.host }}
              </option>
            </template>
          </AppSelect>
        </FormField>
        <div class="flex justify-end gap-2 pt-1">
          <AppButton :disabled="isBusy('create-database')" @click="closeCreateDatabase">Cancel</AppButton>
          <AppButton
            variant="primary"
            type="submit"
            :loading="isBusy('create-database')"
            :disabled="runner.busy.value || !engine || activeAccounts.length === 0"
          >
            Create database
          </AppButton>
        </div>
      </form>
    </AppDialog>

    <!-- Grant access dialog -->
    <AppDialog :open="grantOpen" title="Grant access" @close="grantOpen = false">
      <form class="space-y-4" @submit.prevent="submitGrant">
        <p class="text-[13px] leading-relaxed text-ink-secondary">
          You'll review a plan before the grant takes effect.
        </p>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <FormField label="Database">
          <AppSelect v-model="grantDatabaseId" required empty-message="No active databases — create a database first">
            <template v-if="activeDatabases.length">
              <option disabled value="">Select database</option>
              <option v-for="item in activeDatabases" :key="item.id" :value="item.id">{{ item.name }}</option>
            </template>
          </AppSelect>
        </FormField>
        <FormField label="Account">
          <AppSelect v-model="grantAccountId" required empty-message="No active accounts — create an account first">
            <template v-if="activeAccounts.length">
              <option disabled value="">Select account</option>
              <option v-for="item in activeAccounts" :key="item.id" :value="item.id">
                {{ item.name }}@{{ item.host }}
              </option>
            </template>
          </AppSelect>
        </FormField>
        <FormField label="Access">
          <AppSelect v-model="access">
            <option value="connect">Connect only</option>
            <option value="read_only">Read only</option>
            <option value="read_write">Read and write</option>
          </AppSelect>
        </FormField>
        <div class="flex justify-end gap-2 pt-1">
          <AppButton :disabled="isBusy('create-grant')" @click="grantOpen = false">Cancel</AppButton>
          <AppButton
            variant="primary"
            type="submit"
            :loading="isBusy('create-grant')"
            :disabled="runner.busy.value || activeDatabases.length === 0 || activeAccounts.length === 0"
          >
            Grant access
          </AppButton>
        </div>
      </form>
    </AppDialog>

    <!-- Plan review -->
    <PlanReviewDialog
      v-bind="planDialogProps"
      :busy="runner.busy.value || planRefreshing"
      approve-label="Approve and apply"
      @approve="approvePlan"
      @regenerate="regeneratePlan"
      @close="planDialog = undefined"
    >
      <AppAlert v-if="runner.busy.value && !isBusy('apply')" tone="warning">
        {{ BUSY_MESSAGE }}
      </AppAlert>
      <AppAlert v-if="planDialog?.plan.agentPlan.interruption" tone="danger">
        Applying this plan briefly interrupts active connections.
      </AppAlert>
    </PlanReviewDialog>

    <!-- Restore apply confirm (type-to-confirm at the moment data is replaced) -->
    <AppConfirmDialog
      :open="applyRestoreConfirmOpen"
      :title="restorePlanDatabaseName ? `Restore ${restorePlanDatabaseName}?` : 'Restore database?'"
      confirm-label="Restore database"
      v-bind="restorePlanDatabaseName ? { typeToConfirm: restorePlanDatabaseName } : {}"
      :busy="isBusy('apply')"
      @confirm="applyCurrentPlan"
      @close="applyRestoreConfirmOpen = false"
    >
      Restoring replaces the current data and terminates active connections. Anything written since this backup is
      lost.
    </AppConfirmDialog>

    <!-- Rotate confirm -->
    <AppConfirmDialog
      :open="!!rotateTarget"
      title="Rotate password"
      confirm-label="Rotate password"
      @confirm="confirmRotate"
      @close="rotateTarget = undefined"
    >
      This prepares a plan to replace the password for
      <strong class="text-ink">{{ rotateTarget ? `${rotateTarget.name}@${rotateTarget.host}` : '' }}</strong
      >. The current password keeps working until you approve and apply the plan — then it stops working everywhere and
      the new one is shown exactly once.
    </AppConfirmDialog>

    <!-- Reveal confirm -->
    <AppConfirmDialog
      :open="!!revealTarget"
      title="Reveal password"
      confirm-label="Reveal once"
      tone="accent"
      :busy="revealBusy"
      @confirm="confirmReveal"
      @close="revealTarget = undefined"
    >
      The password for
      <strong class="text-ink">{{ revealTarget ? `${revealTarget.name}@${revealTarget.host}` : '' }}</strong>
      is shown exactly once. After you clear it, it cannot be revealed again — have your password manager ready.
    </AppConfirmDialog>

    <!-- Restore prepare confirm; the destructive apply step above requires typing the name -->
    <AppConfirmDialog
      :open="!!restoreTarget"
      title="Restore database"
      confirm-label="Prepare restore"
      @confirm="confirmRestore"
      @close="restoreTarget = undefined"
    >
      <div class="space-y-3">
        <p>
          Restoring replaces everything currently in
          <strong class="text-ink">{{ restoreTarget ? databaseLabel(restoreTarget.databaseId) : '' }}</strong>
          with the contents of this backup. You'll review a plan before anything changes.
        </p>
        <FactList :facts="restoreFacts" />
      </div>
    </AppConfirmDialog>
  </section>
</template>
