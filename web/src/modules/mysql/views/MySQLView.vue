<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner, type JobMessage } from '@/shared/composables/useJobRunner'
import { usePlanReview, type PlanTarget } from '@/shared/composables/usePlanReview'
import { formatDateTime, formatMeasuredBytes, humanize } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppIcon,
  AppInput,
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

import { useToolLaunch } from '@/modules/admintools/composables/useToolLaunch'
import { useIdentityStore } from '@/modules/identity/store'

import {
  applyPlan,
  createAccount,
  createBackup,
  createDatabase,
  dropAccount,
  dropDatabase,
  getPlan,
  listAccounts,
  listDatabases,
  listEngines,
  revealCredential,
  rotateAccount,
  type Account,
  type Database,
  type Engine,
  type Plan,
  type ResourceType,
} from '../api'

const NAME_RULE = /^[a-z][a-z0-9_]+$/
const NAME_HINT = 'Lowercase letters, numbers, and underscores; starts with a letter.'
const NAME_ERROR = 'Use lowercase letters, numbers, and underscores, starting with a letter (at least 2 characters).'

/** MySQL and MariaDB are separate products sharing one wire protocol; never collapse the two. */
const ENGINE_NAMES: Record<Engine['kind'], string> = { mysql: 'MySQL', mariadb: 'MariaDB' }

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))
const canApply = computed(() => identity.can('operations.apply'))

const enginesQuery = useQuery({ queryKey: ['mysql-engines'], queryFn: listEngines, retry: false })
const accountsQuery = useQuery({ queryKey: ['mysql-accounts'], queryFn: listAccounts, retry: false })
const databasesQuery = useQuery({ queryKey: ['mysql-databases'], queryFn: listDatabases, retry: false })

const engines = computed(() => enginesQuery.data.value ?? [])
/**
 * At most one native engine runs per host, and it is discovered rather than
 * provisioned — there is no create endpoint. Accounts and databases are always
 * created against this one.
 */
const engine = computed(() => engines.value[0])
const accounts = computed(() => accountsQuery.data.value ?? [])
const databases = computed(() => databasesQuery.data.value ?? [])
const activeAccounts = computed(() => accounts.value.filter((item) => item.status === 'active'))

// One runner per operation so only the form or button that launched a job shows
// busy while everything else stays usable.
const accountRunner = useJobRunner()
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
  await Promise.all([enginesQuery.refetch(), accountsQuery.refetch(), databasesQuery.refetch()])
}

const plans = usePlanReview<ResourceType, Plan>({
  loadPlan: getPlan,
  applyPlan,
  refresh: refreshAll,
  canApply: () => identity.can('operations.apply'),
  isDestructive: (target, plan) =>
    plan.operation.toLowerCase().includes('drop') ||
    plan.operation.toLowerCase().includes('revoke') ||
    (target.type === 'restore-points' && (plan.operation.toLowerCase().includes('restore') || plan.agentPlan.interruption)),
})

// One-click phpMyAdmin launch, logged in as the database's owner account.
const toolLaunch = useToolLaunch()

function openPhpMyAdmin(database: Database) {
  if (!identity.can('operations.apply')) return
  void toolLaunch.launch('phpmyadmin', 'mysql', database.id)
}

/** The host is part of an account's identity: `app_usr@localhost` ≠ `app_usr@%`. */
function accountLabel(id: string) {
  const account = accounts.value.find((item) => item.id === id)
  return account ? `${account.name}@${account.host}` : id
}

function engineLabel(id: string) {
  const item = engines.value.find((entry) => entry.id === id)
  return item ? `${ENGINE_NAMES[item.kind]} ${item.version}` : id
}

/** Just the major, so a full semver never overflows the version badge. */
function engineMajor(item: Engine) {
  return item.version.split('.')[0] ?? item.version
}

function detailLink(database: Database) {
  return `/mysql/${encodeURIComponent(database.id)}`
}

/** Sizes are measured on read with a refresh interval, so say when, not just what. */
function sizeTitle(database: Database) {
  return database.sizeObservedAt ? `Measured ${formatDateTime(database.sizeObservedAt)}` : undefined
}

// --- The databases table ---

const collection = useCollection(() => databases.value, {
  searchText: (item) => `${item.name} ${accountLabel(item.ownerAccountId)}`,
  pageSize: 10,
})

// --- Create dialogs ---

const showAccountDialog = ref(false)
const showDatabaseDialog = ref(false)

const accountName = ref('')
const accountHost = ref('localhost')
const accountAttempted = ref(false)

const databaseName = ref('')
const ownerAccountId = ref('')
const databaseAttempted = ref(false)

const accountNameError = computed(() => (accountAttempted.value && !NAME_RULE.test(accountName.value) ? NAME_ERROR : ''))
const accountHostError = computed(() =>
  accountAttempted.value && !accountHost.value.trim() ? 'Enter a host, or % to allow any host.' : '',
)

const databaseNameError = computed(() =>
  databaseAttempted.value && !NAME_RULE.test(databaseName.value) ? NAME_ERROR : '',
)
const ownerAccountError = computed(() =>
  databaseAttempted.value && !ownerAccountId.value ? 'Select an owner account.' : '',
)

const ownerOptions = computed(() => activeAccounts.value.filter((item) => item.engineId === engine.value?.id))

function openAccountDialog() {
  if (!canWrite.value) return
  accountAttempted.value = false
  accountRunner.progress.value = undefined
  accountRunner.error.value = ''
  showAccountDialog.value = true
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

function resetAccountForm() {
  accountName.value = ''
  accountHost.value = 'localhost'
  accountAttempted.value = false
}

function resetDatabaseForm() {
  databaseName.value = ''
  ownerAccountId.value = ''
  databaseAttempted.value = false
}

async function submitAccount() {
  if (!canWrite.value) return
  accountAttempted.value = true
  if (accountNameError.value || accountHostError.value) return
  const activeEngine = engine.value
  if (!activeEngine) return
  let created: PlanTarget<ResourceType> | undefined
  await accountRunner.run(
    async () => {
      const result = await createAccount({
        engineId: activeEngine.id,
        name: accountName.value,
        host: accountHost.value,
      })
      created = { type: 'accounts', id: result.account.id, label: `${result.account.name}@${result.account.host}` }
      return result.job.id
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        showAccountDialog.value = false
        resetAccountForm()
        if (created) await plans.open(created)
      },
      failureMessage: 'Creating the account failed',
    },
  )
}

async function submitDatabase() {
  if (!canWrite.value) return
  databaseAttempted.value = true
  if (databaseNameError.value || ownerAccountError.value) return
  const activeEngine = engine.value
  if (!activeEngine) return
  let created: PlanTarget<ResourceType> | undefined
  await databaseRunner.run(
    async () => {
      const result = await createDatabase({
        engineId: activeEngine.id,
        name: databaseName.value,
        ownerAccountId: ownerAccountId.value,
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
  const plan = plans.plan.value
  if (!resource || !plan) return []
  const facts: Fact[] = [{ label: 'Operation', value: humanize(plan.operation) }]
  switch (resource.type) {
    case 'accounts': {
      const account = accounts.value.find((item) => item.id === resource.id)
      if (!account) return []
      facts.push(
        { label: 'Account', value: `${account.name}@${account.host}`, mono: true },
        { label: 'Credential version', value: `v${account.credentialVersion}` },
      )
      break
    }
    case 'databases': {
      const database = databases.value.find((item) => item.id === resource.id)
      if (!database) return []
      facts.push(
        { label: 'Database', value: database.name, mono: true },
        { label: 'Owner account', value: accountLabel(database.ownerAccountId), mono: true },
      )
      break
    }
    // Grants and restore points are only reviewed from the per-database page.
    default:
      return []
  }
  if (engine.value) facts.push({ label: 'Engine', value: engineLabel(engine.value.id) })
  return facts
})

// --- Row actions ---

const rotateTarget = ref<Account>()
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
  const account = rotateTarget.value
  if (!account || !canWrite.value) return
  rotateTarget.value = undefined
  rotatePendingId.value = account.id
  clearCredential()
  void rotateRunner.run(async () => (await rotateAccount(account.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () =>
      plans.open({ type: 'accounts', id: account.id, label: `Rotate ${account.name}@${account.host}` }),
    failureMessage: 'Rotating the credential failed',
  })
}

function backupDatabase(database: Database) {
  if (!canWrite.value) return
  backupPendingId.value = database.id
  void backupRunner.run(async () => (await createBackup(database.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Backed up ${database.name}`,
    failureMessage: 'The backup failed',
  })
}

// --- Delete (drop) ---
// A drop follows the same review path as everything else: the DELETE request
// produces a plan-ready resource, then the destructive plan is opened for a
// final approval before the schema or account is actually removed.
const dropDatabaseTarget = ref<Database>()
const dropAccountTarget = ref<Account>()

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

function confirmDropAccount() {
  const account = dropAccountTarget.value
  if (!account || !canWrite.value) return
  dropAccountTarget.value = undefined
  void dropRunner.run(async () => (await dropAccount(account.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () =>
      plans.open({ type: 'accounts', id: account.id, label: `Delete ${account.name}@${account.host}` }),
    failureMessage: 'Deleting the account failed',
  })
}

// --- One-time credential reveal ---

const revealTarget = ref<Account>()
const revealBusy = ref(false)
const revealError = ref('')
const credential = ref('')
const revealedAccount = ref<Account>()
const credentialCardEl = ref<HTMLElement>()

function askReveal(account: Account) {
  if (!canApply.value) return
  revealError.value = ''
  revealTarget.value = account
}

function clearCredential() {
  credential.value = ''
  revealedAccount.value = undefined
}

async function confirmReveal() {
  const account = revealTarget.value
  if (!account || !canApply.value) return
  revealBusy.value = true
  revealError.value = ''
  try {
    credential.value = await revealCredential(account.id)
    revealedAccount.value = account
    revealTarget.value = undefined
    await accountsQuery.refetch()
    credentialCardEl.value?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  } catch (caught) {
    revealError.value = caught instanceof Error ? caught.message : 'The credential could not be revealed.'
  } finally {
    revealBusy.value = false
  }
}

const revealFacts = computed<Fact[]>(() => {
  const account = revealedAccount.value
  if (!account) return []
  const facts: Fact[] = [{ label: 'Host', value: account.host, mono: true }]
  const item = engines.value.find((entry) => entry.id === account.engineId)
  if (item) {
    facts.push({ label: 'Engine', value: engineLabel(item.id) })
    facts.push({ label: 'Port', value: String(item.port), mono: true })
    facts.push({ label: 'Socket', value: item.socketPath, mono: true })
  }
  const owned = databases.value.filter((entry) => entry.ownerAccountId === account.id)
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

watch(engine, () => {
  if (ownerAccountId.value && !ownerOptions.value.some((item) => item.id === ownerAccountId.value)) {
    ownerAccountId.value = ''
  }
})
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Managed data layer"
      title="MySQL & MariaDB"
      description="Nexa manages the one native MySQL or MariaDB server on this host. Every change is planned first and runs only after administrator approval. Passwords are shown exactly once after creation or rotation."
    >
      <AppButton v-if="canWrite" variant="primary" icon="plus" :disabled="!engine" @click="openDatabaseDialog">
        New database
      </AppButton>
    </PageHeader>

    <AppAlert v-if="!canWrite" tone="info">Your account has read-only access to MySQL and MariaDB resources.</AppAlert>

    <JobFailureNotice v-if="plans.applyRunner.error.value" v-bind="failureProps(plans.applyRunner)" />
    <JobProgress
      v-if="plans.applyRunner.progress.value"
      :event="plans.applyRunner.progress.value"
      v-bind="progressProps(plans.applyRunner)"
    />

    <div v-if="credential" ref="credentialCardEl" class="rounded-2xl">
      <CredentialReveal
        :credential="credential"
        :account-label="revealedAccount ? `${revealedAccount.name}@${revealedAccount.host}` : ''"
        :facts="revealFacts"
        @clear="clearCredential"
      />
    </div>

    <!-- The engine: a read-only summary strip. It is discovered through its
         local Unix socket rather than provisioned, so there is nothing to
         create here and the databases table below is what this page is for. -->
    <div v-if="enginesQuery.isPending.value" class="space-y-1">
      <SkeletonRow v-for="index in 2" :key="index" />
    </div>
    <AppAlert v-else-if="enginesQuery.isError.value" tone="danger">
      <div class="flex flex-wrap items-center gap-3">
        <span class="min-w-0 flex-1">The MySQL and MariaDB engines could not be loaded.</span>
        <AppButton size="sm" @click="enginesQuery.refetch()">Retry</AppButton>
      </div>
    </AppAlert>
    <EmptyState
      v-else-if="!engines.length"
      icon="server"
      title="No engine discovered"
      description="No active native MySQL or MariaDB server was discovered through its local Unix socket. Install one on this host and it appears here — Nexa does not provision it."
    />
    <div v-else class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <div
        v-for="item in engines"
        :key="item.id"
        class="flex items-center gap-3 rounded-xl border border-outline bg-surface/80 px-3.5 py-3"
      >
        <span
          class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-[13px] font-bold text-accent-300"
        >
          {{ engineMajor(item) }}
        </span>
        <span class="min-w-0 flex-1">
          <strong class="block truncate text-[13px] font-semibold text-ink">{{ engineLabel(item.id) }}</strong>
          <small class="block truncate font-mono text-[11px] text-ink-muted" :title="item.socketPath">
            Port {{ item.port }} · {{ item.socketPath }}
          </small>
        </span>
        <StatusPill :status="item.status" />
      </div>
    </div>

    <!-- Databases: the hero. -->
    <AppCard flush eyebrow="Owned resources" title="Databases">
      <template #actions>
        <AppButton v-if="canWrite" size="sm" icon="plus" :disabled="!engine" @click="openDatabaseDialog">
          New database
        </AppButton>
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
            <span class="min-w-0 flex-1">The phpMyAdmin deployment status could not be loaded.</span>
            <AppButton size="sm" @click="toolLaunch.toolsQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <AppAlert v-if="toolLaunch.error.value" tone="danger">{{ toolLaunch.error.value }}</AppAlert>
        <AppAlert v-if="toolLaunch.blocked.value" tone="info">
          <p>The browser blocked the phpMyAdmin tab.</p>
          <a
            :href="toolLaunch.blocked.value.url"
            target="_blank"
            rel="noopener"
            class="mt-1 inline-flex items-center gap-1.5 font-medium underline underline-offset-2"
          >
            Open phpMyAdmin
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
          description="Each database is owned by one account. Create an account first, then create the database it owns."
        >
          <template #action>
            <AppButton v-if="canWrite" icon="plus" :disabled="!engine" @click="openDatabaseDialog">
              New database
            </AppButton>
          </template>
        </EmptyState>
        <template v-else>
          <ListToolbar
            v-model:search="collection.search.value"
            :count="collection.matching.value"
            count-label="databases"
            placeholder="Search by name or owner"
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
                    <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary">
                      {{ accountLabel(item.ownerAccountId) }}
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
                          :disabled="toolLaunch.availability('phpmyadmin') !== 'ready'"
                          :aria-label="`Open phpMyAdmin for ${item.name}`"
                          :title="
                            toolLaunch.availability('phpmyadmin') === 'ready'
                              ? `Open phpMyAdmin for ${item.name}`
                              : toolLaunch.availability('phpmyadmin') === 'loading'
                                ? 'Checking phpMyAdmin status'
                                : toolLaunch.availability('phpmyadmin') === 'error'
                                  ? 'phpMyAdmin status is unavailable'
                                  : 'Install phpMyAdmin from the Applications page to enable this'
                          "
                          @click="openPhpMyAdmin(item)"
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

    <!-- Accounts stay on this page rather than moving to the per-database
         drill-down: a database cannot be created without an existing owner
         account, so accounts reachable only from a database would deadlock the
         very first one. -->
    <AppCard flush eyebrow="Login identities" title="Accounts">
      <template #actions>
        <AppButton v-if="canWrite" size="sm" icon="plus" :disabled="!engine" @click="openAccountDialog">
          Create account
        </AppButton>
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
        <div v-if="accountsQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="index in 2" :key="index" />
        </div>
        <AppAlert v-else-if="accountsQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center gap-3">
            <span class="min-w-0 flex-1">Accounts could not be loaded.</span>
            <AppButton size="sm" @click="accountsQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <EmptyState
          v-else-if="!accounts.length"
          icon="key"
          title="No accounts yet"
          description="Accounts are the logins apps use to connect. Create one, then create the database it owns."
        >
          <template #action>
            <AppButton v-if="canWrite" icon="plus" :disabled="!engine" @click="openAccountDialog">
              Create account
            </AppButton>
          </template>
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <table class="w-full border-collapse text-left">
            <thead>
              <tr class="border-b border-outline">
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Account</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Credential</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Created</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-outline">
              <tr v-for="account in accounts" :key="account.id">
                <td class="px-3 py-2.5 font-mono text-[13px] font-medium whitespace-nowrap text-ink">
                  {{ account.name }}@{{ account.host }}
                </td>
                <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary tabular-nums">
                  v{{ account.credentialVersion }}
                </td>
                <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                  {{ formatDateTime(account.createdAt) }}
                </td>
                <td class="px-3 py-2.5"><StatusPill :status="account.status" /></td>
                <td class="px-3 py-2.5 text-right">
                  <span class="flex items-center justify-end gap-1">
                    <AppButton
                      v-if="account.status === 'plan_ready'"
                      size="sm"
                      :loading="plans.loadingId.value === account.id"
                      @click="plans.open({ type: 'accounts', id: account.id, label: `${account.name}@${account.host}` })"
                    >
                      Review
                    </AppButton>
                    <AppButton
                      v-if="account.credentialAvailable && canApply"
                      size="sm"
                      icon="key"
                      :disabled="revealBusy"
                      @click="askReveal(account)"
                    >
                      Reveal once
                    </AppButton>
                    <AppButton
                      v-if="canWrite && account.status === 'active'"
                      size="sm"
                      variant="ghost"
                      icon="refresh-cw"
                      :aria-label="`Rotate ${account.name}@${account.host}`"
                      :loading="rotateRunner.busy.value && rotatePendingId === account.id"
                      :disabled="rotateRunner.busy.value"
                      @click="rotateTarget = account"
                    />
                    <AppButton
                      v-if="canWrite && (account.status === 'active' || account.status === 'failed')"
                      size="sm"
                      variant="ghost"
                      icon="trash"
                      :aria-label="`Delete ${account.name}@${account.host}`"
                      :disabled="dropRunner.busy.value"
                      @click="dropAccountTarget = account"
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
    <AppDialog :open="canWrite && showAccountDialog" title="Create account" @close="showAccountDialog = false">
      <form class="space-y-4" novalidate @submit.prevent="submitAccount">
        <FormField label="Account name" :hint="NAME_HINT" :error="accountNameError">
          <AppInput v-model="accountName" autocomplete="off" spellcheck="false" :invalid="!!accountNameError" />
        </FormField>
        <FormField
          label="Host"
          hint="Keep localhost for apps on this server, or use % to allow any host."
          :error="accountHostError"
        >
          <AppInput v-model="accountHost" autocomplete="off" spellcheck="false" :invalid="!!accountHostError" />
        </FormField>
        <JobFailureNotice v-if="accountRunner.error.value" v-bind="failureProps(accountRunner)" />
        <JobProgress
          v-if="accountRunner.progress.value"
          :event="accountRunner.progress.value"
          v-bind="progressProps(accountRunner)"
        />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <AppButton :disabled="accountRunner.busy.value" @click="showAccountDialog = false">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="accountRunner.busy.value" :disabled="!engine">
            Create account
          </AppButton>
        </div>
      </form>
    </AppDialog>

    <AppDialog :open="canWrite && showDatabaseDialog" title="New database" @close="closeDatabaseDialog">
      <form class="space-y-4" novalidate @submit.prevent="submitDatabase">
        <FormField label="Database name" :hint="NAME_HINT" :error="databaseNameError">
          <AppInput v-model="databaseName" autocomplete="off" spellcheck="false" :invalid="!!databaseNameError" />
        </FormField>
        <FormField
          label="Owner account"
          hint="The account that owns this database and gets full access to it."
          :error="ownerAccountError"
        >
          <Combobox v-model="ownerAccountId">
            <ComboboxAnchor as-child>
              <ComboboxTrigger
                :invalid="!!ownerAccountError"
                placeholder="Select owner"
                :label="(
                  (id) => {
                    const item = ownerOptions.find((i) => i.id === id)
                    return item ? `${item.name}@${item.host}` : ''
                  }
                )(ownerAccountId)"
              />
            </ComboboxAnchor>
            <ComboboxList>
              <ComboboxInput placeholder="Search accounts…" />
              <ComboboxEmpty>No active accounts — create one first</ComboboxEmpty>
              <ComboboxGroup>
                <ComboboxItem
                  v-for="item in ownerOptions"
                  :key="item.id"
                  :value="item.id"
                  :text-value="`${item.name}@${item.host}`"
                >
                  {{ item.name }}@{{ item.host }}<ComboboxItemIndicator />
                </ComboboxItem>
              </ComboboxGroup>
            </ComboboxList>
          </Combobox>
        </FormField>
        <JobFailureNotice v-if="databaseRunner.error.value" v-bind="failureProps(databaseRunner)" />
        <JobProgress
          v-if="databaseRunner.progress.value"
          :event="databaseRunner.progress.value"
          v-bind="progressProps(databaseRunner)"
        />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <AppButton :disabled="databaseRunner.busy.value" @click="closeDatabaseDialog">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="databaseRunner.busy.value" :disabled="!engine">
            Create database
          </AppButton>
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
      :title="rotateTarget ? `Rotate credential for ${rotateTarget.name}@${rotateTarget.host}?` : 'Rotate credential?'"
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
      :open="canWrite && !!dropAccountTarget"
      :title="dropAccountTarget ? `Delete account ${dropAccountTarget.name}@${dropAccountTarget.host}?` : 'Delete account?'"
      confirm-label="Delete account"
      tone="danger"
      @confirm="confirmDropAccount"
      @close="dropAccountTarget = undefined"
    >
      Applications signing in as this account will lose access. Databases it owns must keep at least one other user, or the
      deletion is refused.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="canApply && !!revealTarget"
      :title="revealTarget ? `Reveal credential for ${revealTarget.name}@${revealTarget.host}?` : 'Reveal credential?'"
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
