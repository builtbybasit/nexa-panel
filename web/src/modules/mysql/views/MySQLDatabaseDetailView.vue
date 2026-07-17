<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import { useJobRunner, type JobMessage } from '@/shared/composables/useJobRunner'
import { formatBytes, formatDateTime, formatMeasuredBytes, humanize } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppSelect,
  CredentialReveal,
  EmptyState,
  FactList,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  PlanReviewDialog,
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'

import {
  createBackup,
  createGrant,
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
  type Engine,
  type RestorePoint,
} from '../api'
import { usePlanReview } from '../composables/usePlanReview'

const ACCESS_LABELS: Record<Access, string> = {
  connect: 'Connect only',
  read_only: 'Read only',
  read_write: 'Read and write',
}

/** MySQL and MariaDB are separate products sharing one wire protocol; never collapse the two. */
const ENGINE_NAMES: Record<Engine['kind'], string> = { mysql: 'MySQL', mariadb: 'MariaDB' }

const route = useRoute()
const databaseId = computed(() => String(route.params.databaseId ?? ''))

const enginesQuery = useQuery({ queryKey: ['mysql-engines'], queryFn: listEngines, retry: false })
const accountsQuery = useQuery({ queryKey: ['mysql-accounts'], queryFn: listAccounts, retry: false })
const databasesQuery = useQuery({ queryKey: ['mysql-databases'], queryFn: listDatabases, retry: false })
const grantsQuery = useQuery({ queryKey: ['mysql-grants'], queryFn: listGrants, retry: false })
const restorePointsQuery = useQuery({ queryKey: ['mysql-restore-points'], queryFn: listRestorePoints, retry: false })

const engines = computed(() => enginesQuery.data.value ?? [])
const accounts = computed(() => accountsQuery.data.value ?? [])
const database = computed(() => (databasesQuery.data.value ?? []).find((item) => item.id === databaseId.value))
const activeAccounts = computed(() => accounts.value.filter((item) => item.status === 'active'))

/** Everything on this page is scoped to one database; the API lists globally. */
const grants = computed(() => (grantsQuery.data.value ?? []).filter((item) => item.databaseId === databaseId.value))
const restorePoints = computed(() =>
  (restorePointsQuery.data.value ?? []).filter((item) => item.databaseId === databaseId.value),
)

const loading = computed(() => databasesQuery.isPending.value)
const missing = computed(() => !loading.value && !databasesQuery.isError.value && !database.value)

const ownerAccount = computed(() => accounts.value.find((item) => item.id === database.value?.ownerAccountId))
const engine = computed(() => engines.value.find((item) => item.id === database.value?.engineId))

/** The owner plus every granted account — FastPanel's "users of this database". */
const accessRows = computed(() => {
  const rows: { account: Account; access: string; grantId?: string; grantStatus?: string }[] = []
  if (ownerAccount.value) rows.push({ account: ownerAccount.value, access: 'Owner' })
  for (const grant of grants.value) {
    const account = accounts.value.find((item) => item.id === grant.accountId)
    if (!account || account.id === ownerAccount.value?.id) continue
    rows.push({ account, access: ACCESS_LABELS[grant.access], grantId: grant.id, grantStatus: grant.status })
  }
  return rows
})

const grantRunner = useJobRunner()
const rotateRunner = useJobRunner()
const backupRunner = useJobRunner()
const restoreRunner = useJobRunner()

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
  await Promise.all([
    accountsQuery.refetch(),
    databasesQuery.refetch(),
    grantsQuery.refetch(),
    restorePointsQuery.refetch(),
  ])
}

const plans = usePlanReview(refreshAll)

/** The host is part of an account's identity: `app_usr@localhost` ≠ `app_usr@%`. */
function accountLabel(id: string) {
  const account = accounts.value.find((item) => item.id === id)
  return account ? `${account.name}@${account.host}` : id
}

function engineLabel(item: Engine) {
  return `${ENGINE_NAMES[item.kind]} ${item.version}`
}

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

const overviewFacts = computed<Fact[]>(() => {
  const item = database.value
  if (!item) return []
  return [
    { label: 'Owner account', value: accountLabel(item.ownerAccountId), mono: true },
    { label: 'Engine', value: engine.value ? engineLabel(engine.value) : item.engineId },
    {
      label: 'Size',
      value: item.sizeObservedAt
        ? `${formatMeasuredBytes(item.sizeBytes)} · measured ${formatDateTime(item.sizeObservedAt)}`
        : formatMeasuredBytes(item.sizeBytes),
    },
    { label: 'Created', value: formatDateTime(item.createdAt) },
  ]
})

const planFacts = computed<Fact[]>(() => {
  const resource = plans.target.value
  const plan = plans.plan.value
  if (!resource || !plan) return []
  const facts: Fact[] = [{ label: 'Operation', value: humanize(plan.operation) }]
  if (resource.type === 'grants') {
    const grant = grants.value.find((item) => item.id === resource.id)
    if (!grant) return []
    facts.push(
      { label: 'Account', value: accountLabel(grant.accountId), mono: true },
      { label: 'Database', value: database.value?.name ?? '', mono: true },
      { label: 'Access', value: ACCESS_LABELS[grant.access] },
    )
  } else if (resource.type === 'restore-points') {
    const point = restorePoints.value.find((item) => item.id === resource.id)
    if (!point) return []
    facts.push(
      { label: 'Database', value: database.value?.name ?? '', mono: true },
      { label: 'Verified', value: verifiedText(point.verifiedAt) },
      { label: 'Size', value: formatBytes(point.sizeBytes) },
    )
    if (point.sha256) facts.push({ label: 'Checksum', value: point.sha256, mono: true })
  } else if (resource.type === 'accounts') {
    const account = accounts.value.find((item) => item.id === resource.id)
    if (!account) return []
    facts.push(
      { label: 'Account', value: `${account.name}@${account.host}`, mono: true },
      { label: 'Credential version', value: `v${account.credentialVersion}` },
    )
  } else {
    return []
  }
  if (engine.value) facts.push({ label: 'Engine', value: engineLabel(engine.value) })
  return facts
})

// --- Grant access ---

const showGrantDialog = ref(false)
const grantAccountId = ref('')
const access = ref<Access>('read_write')
const grantAttempted = ref(false)

const grantAccountError = computed(() => (grantAttempted.value && !grantAccountId.value ? 'Select an account.' : ''))

/** Accounts on this engine that do not already own or hold a grant on it. */
const grantableAccounts = computed(() =>
  activeAccounts.value.filter(
    (account) =>
      account.engineId === database.value?.engineId &&
      account.id !== database.value?.ownerAccountId &&
      !grants.value.some((grant) => grant.accountId === account.id),
  ),
)

function openGrantDialog() {
  grantAttempted.value = false
  grantRunner.progress.value = undefined
  grantRunner.error.value = ''
  showGrantDialog.value = true
}

async function submitGrant() {
  grantAttempted.value = true
  if (grantAccountError.value) return
  let created: { id: string; label: string } | undefined
  await grantRunner.run(
    async () => {
      const result = await createGrant({
        databaseId: databaseId.value,
        accountId: grantAccountId.value,
        access: access.value,
      })
      created = { id: result.grant.id, label: `${accountLabel(result.grant.accountId)} → ${database.value?.name ?? ''}` }
      return result.job.id
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        showGrantDialog.value = false
        grantAccountId.value = ''
        access.value = 'read_write'
        grantAttempted.value = false
        if (created) await plans.open({ type: 'grants', id: created.id, label: created.label })
      },
      failureMessage: 'Creating the grant failed',
    },
  )
}

// --- Backups and restore ---

const restorePendingId = ref<string>()
const rotatePendingId = ref<string>()

watch(restoreRunner.busy, (busy) => {
  if (!busy) restorePendingId.value = undefined
})
watch(rotateRunner.busy, (busy) => {
  if (!busy) rotatePendingId.value = undefined
})

function backupNow() {
  const item = database.value
  if (!item) return
  void backupRunner.run(async () => (await createBackup(item.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Backed up ${item.name}`,
    failureMessage: 'The backup failed',
  })
}

// Preparing a restore is gated too, not only the apply: the plan itself is
// built against a specific backup, so confirm which one before spending a job.
const restoreTarget = ref<RestorePoint>()

const restoreFacts = computed<Fact[]>(() => {
  const point = restoreTarget.value
  if (!point) return []
  return [
    { label: 'Database', value: database.value?.name ?? '', mono: true },
    { label: 'Verified', value: verifiedText(point.verifiedAt) },
    { label: 'Size', value: formatBytes(point.sizeBytes) },
  ]
})

function confirmPrepareRestore() {
  const point = restoreTarget.value
  if (!point) return
  restoreTarget.value = undefined
  restorePendingId.value = point.id
  void restoreRunner.run(async () => (await prepareRestore(point.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => plans.open({ type: 'restore-points', id: point.id, label: `Restore ${database.value?.name ?? ''}` }),
    failureMessage: 'Preparing the restore failed',
  })
}

const restoreConfirmOpen = ref(false)

function onApprove() {
  // Restoring destroys the current data, so it gets a typed confirmation on top
  // of the plan review that every operation already has.
  if (plans.isRestore.value) {
    restoreConfirmOpen.value = true
    return
  }
  void plans.apply()
}

async function applyAfterConfirm() {
  restoreConfirmOpen.value = false
  await plans.apply()
}

// --- Credential rotation and reveal ---

const rotateTarget = ref<Account>()
const revealTarget = ref<Account>()
const revealBusy = ref(false)
const revealError = ref('')
const credential = ref('')
const revealedAccount = ref<Account>()

function clearCredential() {
  credential.value = ''
  revealedAccount.value = undefined
}

function confirmRotate() {
  const account = rotateTarget.value
  if (!account) return
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

async function confirmReveal() {
  const account = revealTarget.value
  if (!account) return
  revealBusy.value = true
  revealError.value = ''
  try {
    credential.value = await revealCredential(account.id)
    revealedAccount.value = account
    revealTarget.value = undefined
    await accountsQuery.refetch()
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
  if (engine.value) {
    facts.push(
      { label: 'Engine', value: engineLabel(engine.value) },
      { label: 'Port', value: String(engine.value.port), mono: true },
      { label: 'Socket', value: engine.value.socketPath, mono: true },
    )
  }
  if (database.value) facts.push({ label: 'Database', value: database.value.name, mono: true })
  return facts
})
</script>

<template>
  <section class="space-y-6">
    <div v-if="loading" class="space-y-1">
      <SkeletonRow v-for="index in 4" :key="index" />
    </div>

    <AppAlert v-else-if="databasesQuery.isError.value" tone="danger">
      <div class="flex flex-wrap items-center gap-3">
        <span class="min-w-0 flex-1">This database could not be loaded.</span>
        <AppButton size="sm" @click="databasesQuery.refetch()">Retry</AppButton>
      </div>
    </AppAlert>

    <EmptyState
      v-else-if="missing"
      icon="database"
      title="Database not found"
      description="It may have been removed, or the link may be out of date."
    >
      <template #action>
        <RouterLink to="/mysql">
          <AppButton icon="arrow-left">Back to MySQL & MariaDB</AppButton>
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else-if="database">
      <PageHeader
        :breadcrumbs="[{ label: 'MySQL & MariaDB', to: '/mysql' }, { label: database.name }]"
        :title="database.name"
      >
        <StatusPill :status="database.status" />
        <AppButton
          v-if="database.status === 'active'"
          icon="copy"
          :loading="backupRunner.busy.value"
          @click="backupNow"
        >
          Back up now
        </AppButton>
      </PageHeader>

      <JobFailureNotice v-if="plans.applyRunner.error.value" v-bind="failureProps(plans.applyRunner)" />
      <JobProgress
        v-if="plans.applyRunner.progress.value"
        :event="plans.applyRunner.progress.value"
        v-bind="progressProps(plans.applyRunner)"
      />

      <CredentialReveal
        v-if="credential"
        :credential="credential"
        :account-label="revealedAccount ? `${revealedAccount.name}@${revealedAccount.host}` : ''"
        :facts="revealFacts"
        @clear="clearCredential"
      />

      <AppCard eyebrow="Overview" :title="database.name">
        <FactList :facts="overviewFacts" />
        <AppAlert v-if="database.failure" tone="danger" class="mt-4">{{ database.failure }}</AppAlert>
      </AppCard>

      <!-- Access: the owner plus every granted account, which is what FastPanel
           calls the database's users. -->
      <AppCard flush eyebrow="Who can reach this database" title="Access">
        <template #actions>
          <AppButton size="sm" icon="plus" :disabled="database.status !== 'active'" @click="openGrantDialog">
            Grant access
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
          <EmptyState
            v-if="!accessRows.length"
            icon="users"
            title="No access yet"
            description="Grant an account connect, read-only, or read-and-write access to this database."
          />
          <div v-else class="overflow-x-auto">
            <table class="w-full border-collapse text-left">
              <thead>
                <tr class="border-b border-outline">
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Account</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Access</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Created</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                  <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody class="divide-y divide-outline">
                <tr v-for="row in accessRows" :key="row.account.id">
                  <td class="px-3 py-2.5 font-mono text-[13px] font-medium whitespace-nowrap text-ink">
                    {{ row.account.name }}@{{ row.account.host }}
                  </td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">{{ row.access }}</td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                    {{ formatDateTime(row.account.createdAt) }}
                  </td>
                  <td class="px-3 py-2.5"><StatusPill :status="row.grantStatus ?? row.account.status" /></td>
                  <td class="px-3 py-2.5 text-right">
                    <span class="flex items-center justify-end gap-1">
                      <AppButton
                        v-if="row.grantId && row.grantStatus === 'plan_ready'"
                        size="sm"
                        :loading="plans.loadingId.value === row.grantId"
                        @click="
                          plans.open({
                            type: 'grants',
                            id: row.grantId,
                            label: `${row.account.name}@${row.account.host} → ${database.name}`,
                          })
                        "
                      >
                        Review
                      </AppButton>
                      <AppButton
                        v-if="row.account.status === 'plan_ready'"
                        size="sm"
                        :loading="plans.loadingId.value === row.account.id"
                        @click="
                          plans.open({
                            type: 'accounts',
                            id: row.account.id,
                            label: `${row.account.name}@${row.account.host}`,
                          })
                        "
                      >
                        Review
                      </AppButton>
                      <AppButton
                        v-if="row.account.credentialAvailable"
                        size="sm"
                        icon="key"
                        :disabled="revealBusy"
                        @click="revealTarget = row.account"
                      >
                        Reveal once
                      </AppButton>
                      <AppButton
                        v-if="row.account.status === 'active'"
                        size="sm"
                        variant="ghost"
                        icon="refresh-cw"
                        :aria-label="`Rotate ${row.account.name}@${row.account.host}`"
                        :loading="rotateRunner.busy.value && rotatePendingId === row.account.id"
                        :disabled="rotateRunner.busy.value"
                        @click="rotateTarget = row.account"
                      />
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </AppCard>

      <AppCard flush eyebrow="Recovery" title="Restore points">
        <div class="space-y-3 px-3 pb-3 sm:px-4 sm:pb-4">
          <div
            v-if="
              backupRunner.error.value ||
              backupRunner.progress.value ||
              restoreRunner.error.value ||
              restoreRunner.progress.value
            "
            class="space-y-2"
          >
            <JobFailureNotice v-if="backupRunner.error.value" v-bind="failureProps(backupRunner)" />
            <JobProgress
              v-if="backupRunner.progress.value"
              :event="backupRunner.progress.value"
              v-bind="progressProps(backupRunner)"
            />
            <JobFailureNotice v-if="restoreRunner.error.value" v-bind="failureProps(restoreRunner)" />
            <JobProgress
              v-if="restoreRunner.progress.value"
              :event="restoreRunner.progress.value"
              v-bind="progressProps(restoreRunner)"
            />
          </div>
          <div v-if="restorePointsQuery.isPending.value" class="space-y-1">
            <SkeletonRow v-for="index in 2" :key="index" />
          </div>
          <EmptyState
            v-else-if="!restorePoints.length"
            icon="rotate-ccw"
            title="No restore points yet"
            description="Back up this database to create a verified restore point."
          />
          <div v-else class="overflow-x-auto">
            <table class="w-full border-collapse text-left">
              <thead>
                <tr class="border-b border-outline">
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Created</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Size</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Verified</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                  <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody class="divide-y divide-outline">
                <tr v-for="point in restorePoints" :key="point.id">
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                    {{ formatDateTime(point.createdAt) }}
                  </td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary tabular-nums">
                    {{ formatBytes(point.sizeBytes) }}
                  </td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                    {{ point.verifiedAt ? formatDateTime(point.verifiedAt) : 'Pending' }}
                  </td>
                  <td class="px-3 py-2.5"><StatusPill :status="point.status" /></td>
                  <td class="px-3 py-2.5 text-right">
                    <span class="flex items-center justify-end gap-1">
                      <AppButton
                        v-if="point.status === 'plan_ready'"
                        size="sm"
                        :loading="plans.loadingId.value === point.id"
                        @click="plans.open({ type: 'restore-points', id: point.id, label: `Restore ${database.name}` })"
                      >
                        Review
                      </AppButton>
                      <AppButton
                        v-if="point.status === 'verified'"
                        size="sm"
                        variant="danger"
                        :loading="restoreRunner.busy.value && restorePendingId === point.id"
                        :disabled="restoreRunner.busy.value"
                        @click="restoreTarget = point"
                      >
                        Prepare restore
                      </AppButton>
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </AppCard>

      <AppDialog :open="showGrantDialog" title="Grant access" @close="showGrantDialog = false">
        <form class="space-y-4" novalidate @submit.prevent="submitGrant">
          <FormField label="Account" :error="grantAccountError">
            <AppSelect
              v-model="grantAccountId"
              :invalid="!!grantAccountError"
              empty-message="Every active account on this engine already has access"
            >
              <option v-if="grantableAccounts.length" disabled value="">Select account</option>
              <option v-for="account in grantableAccounts" :key="account.id" :value="account.id">
                {{ account.name }}@{{ account.host }}
              </option>
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

      <PlanReviewDialog
        :open="plans.dialogOpen.value"
        :title="plans.target.value?.label ?? 'Review plan'"
        :facts="planFacts"
        :warnings="plans.warnings.value"
        :busy="plans.busy.value || plans.applyRunner.busy.value"
        approve-label="Approve and execute"
        v-bind="plans.dialogProps.value"
        @approve="onApprove"
        @regenerate="plans.regenerate()"
        @close="plans.dialogOpen.value = false"
      />

      <AppConfirmDialog
        :open="!!restoreTarget"
        title="Restore database"
        confirm-label="Prepare restore"
        @confirm="confirmPrepareRestore"
        @close="restoreTarget = undefined"
      >
        <div class="space-y-3">
          <p>
            Restoring replaces everything currently in
            <strong class="text-ink">{{ database.name }}</strong>
            with the contents of this backup. You'll review a plan before anything changes.
          </p>
          <FactList :facts="restoreFacts" />
        </div>
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="restoreConfirmOpen"
        :title="`Restore ${database.name}?`"
        confirm-label="Restore database"
        :type-to-confirm="database.name"
        @confirm="applyAfterConfirm"
        @close="restoreConfirmOpen = false"
      >
        Restoring replaces the current data and terminates active connections. Anything written since this backup is lost.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="!!rotateTarget"
        :title="rotateTarget ? `Rotate credential for ${rotateTarget.name}@${rotateTarget.host}?` : 'Rotate credential?'"
        confirm-label="Rotate credential"
        @confirm="confirmRotate"
        @close="rotateTarget = undefined"
      >
        Connected applications will fail until the new password is deployed. You review a plan before anything changes,
        and the new password is shown exactly once.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="!!revealTarget"
        :title="revealTarget ? `Reveal credential for ${revealTarget.name}@${revealTarget.host}?` : 'Reveal credential?'"
        confirm-label="Reveal now"
        tone="accent"
        :busy="revealBusy"
        @confirm="confirmReveal"
        @close="revealTarget = undefined"
      >
        <p>
          This credential is shown exactly once. Copy or download it before leaving the page — it cannot be revealed
          again.
        </p>
        <AppAlert v-if="revealError" tone="danger" class="mt-3">{{ revealError }}</AppAlert>
      </AppConfirmDialog>
    </template>
  </section>
</template>
