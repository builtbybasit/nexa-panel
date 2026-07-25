<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { useRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { useToasts } from '@/shared/composables/useToasts'
import {
  AppAlert,
  AppButton,
  AppConfirmDialog,
  AppIcon,
  EmptyState,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  SkeletonRow,
  StatusPill,
  Switch,
} from '@/shared/ui'

import { describe as describeCron } from '../../schedules/cron'
import {
  deleteBackupPlan,
  listBackupAccounts,
  listBackupPlans,
  runBackupPlan,
  toggleBackupPlan,
  type BackupPlan,
} from '../api'
import { backupScheduleStatus } from '../status'
import BackupPlanFormDialog from './BackupPlanFormDialog.vue'
import BackupsTabs from './BackupsTabs.vue'

const { push } = useToasts()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('backups.write'))

const plansQuery = useQuery({ queryKey: ['backup-plans'], queryFn: listBackupPlans, retry: false })
const accountsQuery = useQuery({ queryKey: ['backup-accounts'], queryFn: listBackupAccounts, retry: false })

const plans = computed(() => plansQuery.data.value ?? [])
const accounts = computed(() => accountsQuery.data.value ?? [])
const accountName = (id: string) => accounts.value.find((account) => account.id === id)?.name ?? '—'

function scheduleLabel(expression: string) {
  try {
    return describeCron(expression)
  } catch {
    return expression
  }
}

const dialogOpen = ref(false)
const editing = ref<BackupPlan>()

function openCreate() {
  if (!canWrite.value) return
  editing.value = undefined
  dialogOpen.value = true
}
function openEdit(plan: BackupPlan) {
  if (!canWrite.value) return
  editing.value = plan
  dialogOpen.value = true
}
async function onSaved() {
  dialogOpen.value = false
  push({ title: editing.value ? 'Backup plan updated' : 'Backup plan created', tone: 'success' })
  await plansQuery.refetch()
}

const runner = useJobRunner()
const runningId = ref<string>()
async function backupNow(plan: BackupPlan) {
  if (!canWrite.value) return
  runningId.value = plan.id
  await runner.run(async () => (await runBackupPlan(plan.id)).id, {
    successToast: `Backup of ${plan.name} completed`,
    failureMessage: `Backup of ${plan.name} failed`,
    onSettled: async () => {
      await plansQuery.refetch()
    },
  })
  runningId.value = undefined
}

const togglingId = ref<string>()
async function onToggle(plan: BackupPlan, enabled: boolean) {
  if (!canWrite.value) return
  togglingId.value = plan.id
  try {
    await toggleBackupPlan(plan.id, enabled)
    await plansQuery.refetch()
  } catch (caught) {
    push({ title: `Could not update ${plan.name}`, body: caught instanceof Error ? caught.message : undefined, tone: 'danger' })
  } finally {
    togglingId.value = undefined
  }
}

const confirmDelete = ref<BackupPlan>()
const deleting = ref(false)
async function doDelete() {
  if (!canWrite.value) return
  const plan = confirmDelete.value
  if (!plan) return
  deleting.value = true
  try {
    await deleteBackupPlan(plan.id)
    push({ title: `${plan.name} removed`, tone: 'success' })
    confirmDelete.value = undefined
    await plansQuery.refetch()
  } catch (caught) {
    push({ title: `Could not remove ${plan.name}`, body: caught instanceof Error ? caught.message : undefined, tone: 'danger' })
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Backups"
      title="Backup plans"
      description="Each plan backs up the sites and databases you choose to a storage account. The observed schedule state below confirms whether its host timer is actually installed; use Back up now for an immediate run."
    >
      <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">New plan</AppButton>
    </PageHeader>

    <BackupsTabs />

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
    <JobProgress
      v-if="runner.progress.value"
      :event="runner.progress.value"
      v-bind="runner.progressProps.value"
    />

    <div v-if="plansQuery.isPending.value" class="space-y-2">
      <SkeletonRow v-for="n in 3" :key="n" />
    </div>

    <AppAlert v-else-if="plansQuery.isError.value" tone="danger">
      <p>The backup plans couldn't be loaded.</p>
      <AppButton size="sm" class="mt-2" icon="refresh-cw" @click="plansQuery.refetch()">Retry</AppButton>
    </AppAlert>

    <EmptyState
      v-else-if="!plans.length"
      icon="calendar-clock"
      title="No backup plans yet"
      description="Create a plan to back up sites and databases on a schedule."
    >
      <template #action>
        <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">New plan</AppButton>
      </template>
    </EmptyState>

    <ul v-else class="space-y-2">
      <li
        v-for="plan in plans"
        :key="plan.id"
        class="flex flex-wrap items-center gap-4 rounded-xl border border-outline bg-white/[0.02] px-4 py-3"
      >
        <div class="flex items-center gap-2" :title="plan.enabled ? 'Enabled' : 'Disabled'">
          <Switch :model-value="plan.enabled" :disabled="!canWrite || togglingId === plan.id" @update:model-value="onToggle(plan, $event)" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-semibold text-ink">{{ plan.name }}</p>
          <p class="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-ink-muted">
            <span class="inline-flex items-center gap-1"><AppIcon name="hard-drive" :size="12" />{{ accountName(plan.accountId) }}</span>
            <span class="inline-flex items-center gap-1"><AppIcon name="layers" :size="12" />{{ plan.siteIds.length }} site(s)</span>
            <span class="inline-flex items-center gap-1"><AppIcon name="database" :size="12" />{{ plan.databaseIds.length }} db(s)</span>
            <span class="inline-flex items-center gap-1" title="Stored for future retention enforcement"><AppIcon name="archive" :size="12" />retention target {{ plan.copiesLimit }}</span>
            <span class="inline-flex items-center gap-1"><AppIcon name="clock" :size="12" />{{ scheduleLabel(plan.schedule) }}</span>
          </p>
        </div>
        <StatusPill
          :tone="backupScheduleStatus(plan).tone"
          :label="backupScheduleStatus(plan).label"
          :description="backupScheduleStatus(plan).description"
          :pulse="plan.scheduleState === 'pending'"
        />
        <div class="flex shrink-0 items-center gap-2">
          <AppButton
            v-if="canWrite"
            size="sm"
            icon="play"
            :loading="runningId === plan.id"
            :disabled="runner.busy.value"
            @click="backupNow(plan)"
          >
            Back up now
          </AppButton>
          <AppButton size="sm" icon="package" @click="router.push(`/backups/plans/${plan.id}/copies`)">Copies</AppButton>
          <AppButton v-if="canWrite" size="sm" icon="pencil" @click="openEdit(plan)">Edit</AppButton>
          <AppButton v-if="canWrite" size="sm" variant="danger" icon="trash" @click="confirmDelete = plan">Delete</AppButton>
        </div>
      </li>
    </ul>

    <BackupPlanFormDialog
      v-if="dialogOpen && canWrite"
      :plan="editing"
      :accounts="accounts"
      @saved="onSaved"
      @close="dialogOpen = false"
    />

    <AppConfirmDialog
      :open="canWrite && !!confirmDelete"
      title="Delete backup plan"
      :confirm-label="`Delete ${confirmDelete?.name ?? ''}`"
      tone="danger"
      :busy="deleting"
      @confirm="doDelete"
      @close="confirmDelete = undefined"
    >
      Removing <strong>{{ confirmDelete?.name }}</strong> stops its schedule. A plan can only be deleted after all of its
      recorded copies have been removed.
    </AppConfirmDialog>
  </section>
</template>
