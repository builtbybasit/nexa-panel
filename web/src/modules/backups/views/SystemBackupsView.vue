<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { formatBytes, formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppIcon,
  EmptyState,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  SkeletonRow,
} from '@/shared/ui'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'

import { listBackupAccounts, listSystemBackupCopies, runSystemBackup } from '../api'
import BackupsTabs from './BackupsTabs.vue'

const identity = useIdentityStore()
const canWrite = computed(() => identity.can('backups.write'))

const accountsQuery = useQuery({ queryKey: ['backup-accounts'], queryFn: listBackupAccounts, retry: false })
const accounts = computed(() => accountsQuery.data.value ?? [])

const copiesQuery = useQuery({ queryKey: ['system-backup-copies'], queryFn: listSystemBackupCopies, retry: false })
const copies = computed(() => copiesQuery.data.value ?? [])

// Default the destination to the first account once loaded; the operator can
// still pick another before running.
const selectedAccountId = ref('')
watch(
  accounts,
  (list) => {
    if (!selectedAccountId.value && list.length) selectedAccountId.value = list[0]!.id
  },
  { immediate: true },
)

const runner = useJobRunner()

async function runBackup() {
  if (!canWrite.value || !selectedAccountId.value) return
  await runner.run(async () => (await runSystemBackup(selectedAccountId.value)).id, {
    successToast: 'Panel state backed up',
    failureMessage: 'Panel-state backup failed',
    onSettled: async () => {
      await copiesQuery.refetch()
    },
  })
}

function accountName(id: string) {
  return accounts.value.find((account) => account.id === id)?.name ?? id
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Backups"
      title="Panel state"
      description="Back up the panel's own state — control.db and master.key — to a storage account off this server. Without it, a lost disk takes every stored site and database credential with it."
    >
      <AppButton
        v-if="canWrite"
        variant="primary"
        icon="shield"
        :disabled="!selectedAccountId || runner.busy.value"
        :loading="runner.busy.value"
        @click="runBackup"
      >
        Back up now
      </AppButton>
    </PageHeader>

    <BackupsTabs />

    <AppAlert tone="warning">
      This archive bundles <strong>master.key</strong> and the encrypted <strong>control.db</strong> together — anyone who
      holds it can decrypt every stored credential. Only send it to a storage account you trust and that is itself
      encrypted. Restore is a deliberate command-line step, documented in
      <span class="font-mono">docs/runbooks/panel-state-restore.md</span>.
    </AppAlert>

    <!-- destination + run -->
    <div class="rounded-xl border border-outline bg-white/[0.02] p-4">
      <div v-if="accountsQuery.isPending.value" class="space-y-2">
        <SkeletonRow />
      </div>
      <EmptyState
        v-else-if="!accounts.length"
        icon="archive"
        title="No storage accounts yet"
        description="Panel-state backups reuse the same storage destinations as plans. Create one first."
      >
        <template #action>
          <AppButton icon="plus" @click="$router.push('/backups/accounts')">Add an account</AppButton>
        </template>
      </EmptyState>
      <FormField v-else label="Destination" hint="Where the panel-state archive is shipped.">
        <div class="flex flex-wrap items-center gap-3">
          <Select v-model="selectedAccountId" :disabled="!canWrite">
            <SelectTrigger class="min-w-56" />
            <SelectContent>
              <SelectItem v-for="account in accounts" :key="account.id" :value="account.id">{{ account.name }}</SelectItem>
            </SelectContent>
          </Select>
          <AppButton
            v-if="canWrite"
            variant="primary"
            icon="shield"
            :disabled="!selectedAccountId || runner.busy.value"
            :loading="runner.busy.value"
            @click="runBackup"
          >
            Back up now
          </AppButton>
        </div>
      </FormField>
    </div>

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
    <JobProgress
      v-if="runner.progress.value"
      :event="runner.progress.value"
      v-bind="runner.progressProps.value"
    />

    <!-- copies -->
    <div v-if="copiesQuery.isPending.value" class="space-y-2">
      <SkeletonRow v-for="n in 3" :key="n" />
    </div>

    <AppAlert v-else-if="copiesQuery.isError.value" tone="danger">
      <p>The panel-state backups couldn't be loaded.</p>
      <AppButton size="sm" class="mt-2" icon="refresh-cw" @click="copiesQuery.refetch()">Retry</AppButton>
    </AppAlert>

    <EmptyState
      v-else-if="!copies.length"
      icon="shield"
      title="No panel-state backups yet"
      description="Run one now so the panel's own credentials can survive a lost disk."
    />

    <ul v-else class="space-y-2">
      <li
        v-for="copy in copies"
        :key="copy.id"
        class="flex flex-wrap items-center gap-4 rounded-xl border border-outline bg-white/[0.02] px-4 py-3"
      >
        <span class="grid size-10 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-ink-muted">
          <AppIcon name="shield" :size="18" />
        </span>
        <div class="min-w-0 flex-1">
          <p class="truncate font-mono text-sm font-semibold text-ink">{{ copy.copyName }}</p>
          <p class="flex flex-wrap items-center gap-x-3 text-xs text-ink-muted">
            <span>{{ formatDateTime(copy.createdAt) }}</span>
            <span>{{ formatBytes(copy.sizeBytes) }}</span>
            <span>{{ accountName(copy.accountId) }}</span>
            <span class="truncate font-mono">{{ copy.remotePath }}</span>
          </p>
        </div>
      </li>
    </ul>
  </section>
</template>
