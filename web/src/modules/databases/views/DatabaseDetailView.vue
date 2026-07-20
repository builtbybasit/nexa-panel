<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import { useJobRunner, type JobMessage } from '@/shared/composables/useJobRunner'
import { usePlanReview } from '@/shared/composables/usePlanReview'
import { useIdentityStore } from '@/modules/identity/store'
import { formatBytes, formatDateTime, formatMeasuredBytes } from '@/shared/formatters'
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
  applyPlan,
  createBackup,
  createGrant,
  dropGrant,
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
  type PostgresPlan,
  type ResourceType,
  type RestorePoint,
} from '../api'

const ACCESS_LABELS: Record<AccessLevel, string> = {
  connect: 'Connect only',
  read_only: 'Read only',
  read_write: 'Read and write',
}

const route = useRoute()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))
const canApply = computed(() => identity.can('operations.apply'))
const databaseId = computed(() => String(route.params.databaseId ?? ''))

const instancesQuery = useQuery({ queryKey: ['postgresql-instances'], queryFn: listInstances, retry: false })
const rolesQuery = useQuery({ queryKey: ['postgresql-roles'], queryFn: listRoles, retry: false })
const databasesQuery = useQuery({ queryKey: ['postgresql-databases'], queryFn: listDatabases, retry: false })
const grantsQuery = useQuery({ queryKey: ['postgresql-grants'], queryFn: listGrants, retry: false })
const restorePointsQuery = useQuery({ queryKey: ['postgresql-restore-points'], queryFn: listRestorePoints, retry: false })

const instances = computed(() => instancesQuery.data.value ?? [])
const roles = computed(() => rolesQuery.data.value ?? [])
const database = computed(() => (databasesQuery.data.value ?? []).find((item) => item.id === databaseId.value))
const activeRoles = computed(() => roles.value.filter((item) => item.status === 'active'))

/** Everything on this page is scoped to one database; the API lists globally. */
const grants = computed(() => (grantsQuery.data.value ?? []).filter((item) => item.databaseId === databaseId.value))
const restorePoints = computed(() =>
  (restorePointsQuery.data.value ?? []).filter((item) => item.databaseId === databaseId.value),
)

const loading = computed(() => databasesQuery.isPending.value)
const missing = computed(() => !loading.value && !databasesQuery.isError.value && !database.value)

const ownerRole = computed(() => roles.value.find((item) => item.id === database.value?.ownerRoleId))
const instance = computed(() => instances.value.find((item) => item.id === database.value?.instanceId))

/** The owner plus every granted role — FastPanel's "users of this database". */
const accessRows = computed(() => {
  const rows: { role: DatabaseRole; access: string; grantId?: string; grantStatus?: string }[] = []
  if (ownerRole.value) rows.push({ role: ownerRole.value, access: 'Owner' })
  for (const grant of grants.value) {
    const role = roles.value.find((item) => item.id === grant.roleId)
    if (!role || role.id === ownerRole.value?.id) continue
    rows.push({ role, access: ACCESS_LABELS[grant.access], grantId: grant.id, grantStatus: grant.status })
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
    rolesQuery.refetch(),
    databasesQuery.refetch(),
    grantsQuery.refetch(),
    restorePointsQuery.refetch(),
  ])
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

const dropRunner = useJobRunner()
const dropGrantTarget = ref<{ id: string; label: string }>()

function confirmDropGrant() {
  const grant = dropGrantTarget.value
  if (!grant || !canWrite.value) return
  dropGrantTarget.value = undefined
  void dropRunner.run(async () => (await dropGrant(grant.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => plans.open({ type: 'grants', id: grant.id, label: `Revoke ${grant.label}` }),
    failureMessage: 'Revoking access failed',
  })
}

function roleLabel(id: string) {
  return roles.value.find((role) => role.id === id)?.name ?? id
}

const overviewFacts = computed<Fact[]>(() => {
  const item = database.value
  if (!item) return []
  const facts: Fact[] = [
    { label: 'Owner role', value: roleLabel(item.ownerRoleId), mono: true },
    {
      label: 'Instance',
      value: instance.value ? `PostgreSQL ${instance.value.version} · ${instance.value.cluster}` : item.instanceId,
    },
    {
      label: 'Size',
      value: item.sizeObservedAt
        ? `${formatMeasuredBytes(item.sizeBytes)} · measured ${formatDateTime(item.sizeObservedAt)}`
        : formatMeasuredBytes(item.sizeBytes),
    },
    { label: 'Created', value: formatDateTime(item.createdAt) },
  ]
  return facts
})

const planFacts = computed<Fact[]>(() => {
  const resource = plans.target.value
  if (!resource) return []
  if (resource.type === 'grants') {
    const grant = grants.value.find((item) => item.id === resource.id)
    if (!grant) return []
    return [
      { label: 'Role', value: roleLabel(grant.roleId), mono: true },
      { label: 'Database', value: database.value?.name ?? '', mono: true },
      { label: 'Access', value: ACCESS_LABELS[grant.access] },
    ]
  }
  if (resource.type === 'restore-points') {
    const point = restorePoints.value.find((item) => item.id === resource.id)
    if (!point) return []
    return [
      { label: 'Database', value: database.value?.name ?? '', mono: true },
      { label: 'Size', value: formatBytes(point.sizeBytes) },
      { label: 'Verified', value: point.verifiedAt ? formatDateTime(point.verifiedAt) : 'Not verified yet' },
    ]
  }
  if (resource.type === 'roles') {
    const role = roles.value.find((item) => item.id === resource.id)
    if (!role) return []
    return [
      { label: 'Role', value: role.name, mono: true },
      { label: 'Credential version', value: `v${role.credentialVersion}` },
    ]
  }
  return []
})

// --- Grant access ---

const showGrantDialog = ref(false)
const grantRoleId = ref('')
const access = ref<AccessLevel>('read_write')
const grantAttempted = ref(false)

const grantRoleError = computed(() => (grantAttempted.value && !grantRoleId.value ? 'Select a role.' : ''))

/** Roles on this instance that do not already own or hold a grant on it. */
const grantableRoles = computed(() =>
  activeRoles.value.filter(
    (role) =>
      role.instanceId === database.value?.instanceId &&
      role.id !== database.value?.ownerRoleId &&
      !grants.value.some((grant) => grant.roleId === role.id),
  ),
)

function openGrantDialog() {
  if (!canWrite.value) return
  grantAttempted.value = false
  grantRunner.progress.value = undefined
  grantRunner.error.value = ''
  showGrantDialog.value = true
}

async function submitGrant() {
  if (!canWrite.value) return
  grantAttempted.value = true
  if (grantRoleError.value) return
  let created: { id: string; label: string } | undefined
  await grantRunner.run(
    async () => {
      const result = await createGrant({ databaseId: databaseId.value, roleId: grantRoleId.value, access: access.value })
      created = { id: result.grant.id, label: `${roleLabel(result.grant.roleId)} → ${database.value?.name ?? ''}` }
      return result.job.id
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        showGrantDialog.value = false
        grantRoleId.value = ''
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
  if (!item || !canWrite.value) return
  void backupRunner.run(async () => (await createBackup(item.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Backed up ${item.name}`,
    failureMessage: 'The backup failed',
  })
}

function prepareRestorePlan(point: RestorePoint) {
  if (!canWrite.value) return
  restorePendingId.value = point.id
  void restoreRunner.run(async () => (await prepareRestore(point.id)).job.id, {
    onSettled: refreshAll,
    onSuccess: () => plans.open({ type: 'restore-points', id: point.id, label: `Restore ${database.value?.name ?? ''}` }),
    failureMessage: 'Preparing the restore failed',
  })
}

const restoreConfirmOpen = ref(false)

function onApprove() {
  if (!canApply.value) return
  // Restoring destroys the current data, so it gets a typed confirmation on top
  // of the plan review that every operation already has.
  if (plans.isDestructive.value) {
    restoreConfirmOpen.value = true
    return
  }
  void plans.apply()
}

async function applyAfterConfirm() {
  if (!canApply.value) return
  restoreConfirmOpen.value = false
  await plans.apply()
}

// --- Credential rotation and reveal ---

const rotateTarget = ref<DatabaseRole>()
const revealTarget = ref<DatabaseRole>()
const revealBusy = ref(false)
const revealError = ref('')
const credential = ref('')
const revealedRole = ref<DatabaseRole>()

function clearCredential() {
  credential.value = ''
  revealedRole.value = undefined
}

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
  } catch (caught) {
    revealError.value = caught instanceof Error ? caught.message : 'The credential could not be revealed.'
  } finally {
    revealBusy.value = false
  }
}

const revealFacts = computed<Fact[]>(() => {
  const role = revealedRole.value
  if (!role || !instance.value) return []
  const facts: Fact[] = [{ label: 'Instance', value: `PostgreSQL ${instance.value.version} · ${instance.value.cluster}` }]
  if (instance.value.port) facts.push({ label: 'Port', value: String(instance.value.port), mono: true })
  else facts.push({ label: 'Socket', value: instance.value.socketPath, mono: true })
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
        <RouterLink to="/databases">
          <AppButton icon="arrow-left">Back to PostgreSQL</AppButton>
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else-if="database">
      <PageHeader
        :breadcrumbs="[{ label: 'PostgreSQL', to: '/databases' }, { label: database.name }]"
        :title="database.name"
      >
        <StatusPill :status="database.status" />
        <AppButton
          v-if="canWrite && database.status === 'active'"
          icon="copy"
          :loading="backupRunner.busy.value"
          @click="backupNow"
        >
          Back up now
        </AppButton>
      </PageHeader>

      <AppAlert v-if="!canWrite" tone="info">Your account has read-only access to this database.</AppAlert>

      <JobFailureNotice v-if="plans.applyRunner.error.value" v-bind="failureProps(plans.applyRunner)" />
      <JobProgress
        v-if="plans.applyRunner.progress.value"
        :event="plans.applyRunner.progress.value"
        v-bind="progressProps(plans.applyRunner)"
      />

      <CredentialReveal
        v-if="credential"
        :credential="credential"
        :account-label="revealedRole?.name ?? ''"
        :facts="revealFacts"
        @clear="clearCredential"
      />

      <AppCard eyebrow="Overview" :title="database.name">
        <FactList :facts="overviewFacts" />
        <AppAlert v-if="database.failure" tone="danger" class="mt-4">{{ database.failure }}</AppAlert>
      </AppCard>

      <!-- Access: the owner plus every granted role, which is what FastPanel
           calls the database's users. -->
      <AppCard flush eyebrow="Who can reach this database" title="Access">
        <template #actions>
          <AppButton
            v-if="canWrite"
            size="sm"
            icon="plus"
            :disabled="database.status !== 'active'"
            @click="openGrantDialog"
          >
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
            description="Grant a role connect, read-only, or read-and-write access to this database."
          />
          <div v-else class="overflow-x-auto">
            <table class="w-full border-collapse text-left">
              <thead>
                <tr class="border-b border-outline">
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Role</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Access</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Created</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                  <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody class="divide-y divide-outline">
                <tr v-for="row in accessRows" :key="row.role.id">
                  <td class="px-3 py-2.5 font-mono text-[13px] font-medium whitespace-nowrap text-ink">
                    {{ row.role.name }}
                  </td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">{{ row.access }}</td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                    {{ formatDateTime(row.role.createdAt) }}
                  </td>
                  <td class="px-3 py-2.5"><StatusPill :status="row.grantStatus ?? row.role.status" /></td>
                  <td class="px-3 py-2.5 text-right">
                    <span class="flex items-center justify-end gap-1">
                      <AppButton
                        v-if="row.grantId && row.grantStatus === 'plan_ready'"
                        size="sm"
                        :loading="plans.loadingId.value === row.grantId"
                        @click="plans.open({ type: 'grants', id: row.grantId, label: `${row.role.name} → ${database.name}` })"
                      >
                        Review
                      </AppButton>
                      <AppButton
                        v-if="row.role.status === 'plan_ready'"
                        size="sm"
                        :loading="plans.loadingId.value === row.role.id"
                        @click="plans.open({ type: 'roles', id: row.role.id, label: row.role.name })"
                      >
                        Review
                      </AppButton>
                      <AppButton
                        v-if="row.role.credentialAvailable && canApply"
                        size="sm"
                        icon="key"
                        @click="revealTarget = row.role"
                      >
                        Reveal once
                      </AppButton>
                      <AppButton
                        v-if="canWrite && row.role.status === 'active'"
                        size="sm"
                        variant="ghost"
                        icon="refresh-cw"
                        :aria-label="`Rotate ${row.role.name}`"
                        :loading="rotateRunner.busy.value && rotatePendingId === row.role.id"
                        :disabled="rotateRunner.busy.value"
                        @click="rotateTarget = row.role"
                      />
                      <AppButton
                        v-if="canWrite && row.grantId && (row.grantStatus === 'active' || row.grantStatus === 'failed')"
                        size="sm"
                        variant="ghost"
                        icon="trash"
                        :aria-label="`Revoke ${row.role.name}`"
                        :disabled="dropRunner.busy.value"
                        @click="dropGrantTarget = { id: row.grantId, label: `${row.role.name} → ${database.name}` }"
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
                        v-if="canWrite && point.status === 'verified'"
                        size="sm"
                        variant="danger"
                        :loading="restoreRunner.busy.value && restorePendingId === point.id"
                        :disabled="restoreRunner.busy.value"
                        @click="prepareRestorePlan(point)"
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

      <AppDialog :open="canWrite && showGrantDialog" title="Grant access" @close="showGrantDialog = false">
        <form class="space-y-4" novalidate @submit.prevent="submitGrant">
          <FormField label="Role" :error="grantRoleError">
            <AppSelect
              v-model="grantRoleId"
              :invalid="!!grantRoleError"
              empty-message="Every active role on this instance already has access"
            >
              <option v-if="grantableRoles.length" disabled value="">Select role</option>
              <option v-for="role in grantableRoles" :key="role.id" :value="role.id">{{ role.name }}</option>
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
        :can-approve="canApply"
        approve-label="Approve and execute"
        v-bind="plans.dialogProps.value"
        @approve="onApprove"
        @regenerate="plans.regenerate()"
        @close="plans.dialogOpen.value = false"
      />

      <AppConfirmDialog
        :open="canApply && restoreConfirmOpen"
        :title="`Restore ${database.name}?`"
        confirm-label="Restore database"
        :type-to-confirm="database.name"
        @confirm="applyAfterConfirm"
        @close="restoreConfirmOpen = false"
      >
        Restoring replaces the current data and terminates active connections. Anything written since this backup is lost.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="canWrite && !!rotateTarget"
        :title="rotateTarget ? `Rotate credential for ${rotateTarget.name}?` : 'Rotate credential?'"
        confirm-label="Rotate credential"
        @confirm="confirmRotate"
        @close="rotateTarget = undefined"
      >
        Connected applications will fail until the new password is deployed. You review a plan before anything changes,
        and the new password is shown exactly once.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="canWrite && !!dropGrantTarget"
        :title="dropGrantTarget ? `Revoke ${dropGrantTarget.label}?` : 'Revoke access?'"
        confirm-label="Revoke access"
        tone="danger"
        @confirm="confirmDropGrant"
        @close="dropGrantTarget = undefined"
      >
        The role keeps its login but loses all access to this database. You review a final plan before it takes effect.
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
        <p>
          This credential is shown exactly once. Copy or download it before leaving the page — it cannot be revealed
          again.
        </p>
        <AppAlert v-if="revealError" tone="danger" class="mt-3">{{ revealError }}</AppAlert>
      </AppConfirmDialog>
    </template>
  </section>
</template>
