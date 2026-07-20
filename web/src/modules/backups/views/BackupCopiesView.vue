<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { useToasts } from '@/shared/composables/useToasts'
import { formatBytes, formatDateTime } from '@/shared/formatters'
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
} from '@/shared/ui'

import {
  deleteBackupCopy,
  listBackupCopies,
  listBackupPlans,
  restoreBackupCopy,
  type BackupCopy,
  type BackupRestoreRequest,
} from '../api'
import { backupCopyHealth } from '../status'
import BackupRestoreDialog from './BackupRestoreDialog.vue'

const route = useRoute()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('backups.write'))
const canRestore = computed(() => identity.can('operations.apply'))
const planId = computed(() => String(route.params.planId ?? ''))

const { push } = useToasts()

const plansQuery = useQuery({ queryKey: ['backup-plans'], queryFn: listBackupPlans, retry: false })
const plan = computed(() => (plansQuery.data.value ?? []).find((entry) => entry.id === planId.value))

const copiesQuery = useQuery({
  queryKey: ['backup-copies', planId],
  queryFn: () => listBackupCopies(planId.value),
  retry: false,
})
const copies = computed(() => copiesQuery.data.value ?? [])

const restoreRunner = useJobRunner()
const restoring = ref<BackupCopy>()

async function submitRestore(body: BackupRestoreRequest) {
  const copy = restoring.value
  if (!copy || !canRestore.value) return
  await restoreRunner.run(async () => (await restoreBackupCopy(copy.id, body)).id, {
    successToast: `Restored ${copy.copyName}`,
    failureMessage: `Restore of ${copy.copyName} failed`,
    onSettled: async () => {
      restoring.value = undefined
    },
  })
}

const confirmDelete = ref<BackupCopy>()
const deleting = ref(false)
async function doDelete() {
  const copy = confirmDelete.value
  if (!copy || !canWrite.value) return
  deleting.value = true
  try {
    await deleteBackupCopy(copy.id)
    push({ title: `Deleted ${copy.copyName}`, tone: 'success' })
    confirmDelete.value = undefined
    await copiesQuery.refetch()
  } catch (caught) {
    push({ title: `Could not delete ${copy.copyName}`, body: caught instanceof Error ? caught.message : undefined, tone: 'danger' })
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Backups"
      :title="plan ? `Copies · ${plan.name}` : 'Backup copies'"
      description="The backup copies stored for this plan, newest first. Health is based on recorded integrity and restore verification, never on upload success alone."
      :breadcrumbs="[
        { label: 'Backups', to: '/backups' },
        { label: plan?.name ?? 'Plan' },
      ]"
    >
      <AppButton icon="arrow-left" @click="$router.push('/backups')">Back to plans</AppButton>
    </PageHeader>

    <JobFailureNotice v-if="restoreRunner.error.value" :message="restoreRunner.error.value" :job-id="restoreRunner.jobId.value" />
    <JobProgress
      v-if="restoreRunner.progress.value"
      :event="restoreRunner.progress.value"
      :messages="restoreRunner.messages.value"
      :started-at-ms="restoreRunner.startedAtMs.value"
    />

    <div v-if="copiesQuery.isPending.value" class="space-y-2">
      <SkeletonRow v-for="n in 3" :key="n" />
    </div>

    <AppAlert v-else-if="copiesQuery.isError.value" tone="danger">
      <p>The backup copies couldn't be loaded.</p>
      <AppButton size="sm" class="mt-2" icon="refresh-cw" @click="copiesQuery.refetch()">Retry</AppButton>
    </AppAlert>

    <EmptyState
      v-else-if="!copies.length"
      icon="archive"
      title="No backup copies yet"
      description="Run this plan from the plans page to create the first copy."
    />

    <ul v-else class="space-y-2">
      <li
        v-for="copy in copies"
        :key="copy.id"
        class="flex flex-wrap items-center gap-4 rounded-xl border border-outline bg-white/[0.02] px-4 py-3"
      >
        <span class="grid size-10 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-ink-muted">
          <AppIcon name="package" :size="18" />
        </span>
        <div class="min-w-0 flex-1">
          <p class="truncate font-mono text-sm font-semibold text-ink">{{ copy.copyName }}</p>
          <p class="flex flex-wrap items-center gap-x-3 text-xs text-ink-muted">
            <span>{{ formatDateTime(copy.createdAt) }}</span>
            <span>{{ formatBytes(copy.sizeBytes) }}</span>
            <span>{{ copy.entries.length }} item(s)</span>
          </p>
        </div>
        <StatusPill
          :tone="backupCopyHealth(copy).tone"
          :label="backupCopyHealth(copy).label"
          :description="backupCopyHealth(copy).description"
          :pulse="false"
        />
        <div class="flex shrink-0 items-center gap-2">
          <AppButton
            v-if="canRestore"
            size="sm"
            icon="rotate-ccw"
            :disabled="restoreRunner.busy.value"
            @click="restoring = copy"
          >
            Restore
          </AppButton>
          <AppButton
            v-if="canWrite"
            size="sm"
            variant="danger"
            icon="trash"
            @click="confirmDelete = copy"
          >
            Delete
          </AppButton>
        </div>
      </li>
    </ul>

    <BackupRestoreDialog
      v-if="restoring && canRestore"
      :copy="restoring"
      :busy="restoreRunner.busy.value"
      @restore="submitRestore"
      @close="restoring = undefined"
    />

    <AppConfirmDialog
      :open="canWrite && !!confirmDelete"
      title="Delete backup copy"
      :confirm-label="`Delete ${confirmDelete?.copyName ?? ''}`"
      tone="danger"
      :busy="deleting"
      @confirm="doDelete"
      @close="confirmDelete = undefined"
    >
      This permanently removes <strong class="font-mono">{{ confirmDelete?.copyName }}</strong> from its storage account.
      This cannot be undone.
    </AppConfirmDialog>
  </section>
</template>
