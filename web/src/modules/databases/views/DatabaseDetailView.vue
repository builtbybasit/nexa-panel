<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useJobRunner } from '@/shared/composables/useJobRunner'
import { formatBytes, formatDateTime, formatMeasuredBytes } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  ConnectionDetails,
  EmptyState,
  FactList,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  SkeletonRow,
  StatusPill,
  type Fact,
} from '@/shared/ui'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'
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
  createBackup,
  createGrant,
  dropDatabase,
  dropGrant,
  listGrants,
  listRestorePoints,
  restoreBackup,
  type AccessLevel,
  type DatabaseUser,
  type RestorePoint,
} from '../api'
import { useDatabasesData } from '../composables/useDatabasesData'
import { connectionEngine, ENGINES, serverLabel, userLabel } from '../lib/engines'

const ACCESS_LABELS: Record<AccessLevel, string> = {
  connect: 'Connect only',
  read_only: 'Read only',
  read_write: 'Read and write',
}

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))
const canLaunch = computed(() => identity.can('operations.apply'))
const databaseId = computed(() => String(route.params.databaseId ?? ''))

const data = useDatabasesData()
const grantsQuery = useQuery({ queryKey: ['database-grants'], queryFn: listGrants, retry: false })
const restorePointsQuery = useQuery({ queryKey: ['database-restore-points'], queryFn: listRestorePoints, retry: false })

const database = computed(() => data.databases.value.find((item) => item.id === databaseId.value))

/** Everything on this page is scoped to one database; the API lists globally. */
const grants = computed(() => (grantsQuery.data.value ?? []).filter((item) => item.databaseId === databaseId.value))
const restorePoints = computed(() =>
  (restorePointsQuery.data.value ?? []).filter((item) => item.databaseId === databaseId.value),
)

const loading = computed(() => data.databasesQuery.isPending.value)
const missing = computed(() => !loading.value && !data.databasesQuery.isError.value && !database.value)

const owner = computed(() => (database.value ? data.user(database.value.ownerUserId) : undefined))
const server = computed(() => (database.value ? data.server(database.value.serverId) : undefined))

/** The owner plus every granted user — FastPanel's "users of this database". */
const accessRows = computed(() => {
  const rows: { user: DatabaseUser; access: string; grantId?: string; grantStatus?: string }[] = []
  if (owner.value) rows.push({ user: owner.value, access: 'Owner' })
  for (const grant of grants.value) {
    const user = data.user(grant.userId)
    if (!user || user.id === owner.value?.id) continue
    rows.push({ user, access: ACCESS_LABELS[grant.access], grantId: grant.id, grantStatus: grant.status })
  }
  return rows
})

/** Users a desktop client could sign in as: the owner first, then the grantees. */
const connectionUsers = computed(() => accessRows.value.map((row) => ({ value: row.user.name, label: userLabel(row.user) })))

const showConnection = ref(false)
const canConnect = computed(
  () => database.value?.status === 'active' && !!server.value && connectionUsers.value.length > 0,
)

const grantRunner = useJobRunner()
const backupRunner = useJobRunner()
const restoreRunner = useJobRunner()
const dropRunner = useJobRunner()

async function refreshAll() {
  await Promise.all([data.refreshAll(), grantsQuery.refetch(), restorePointsQuery.refetch()])
}

// One-click phpMyAdmin/pgAdmin launch, logged in as the owner.
const toolLaunch = useToolLaunch()

function openWebClient() {
  const item = database.value
  if (!item || !canLaunch.value) return
  void toolLaunch.launch(ENGINES[item.engine].tool, item.engine, item.id)
}

const overviewFacts = computed<Fact[]>(() => {
  const item = database.value
  if (!item) return []
  return [
    { label: 'Owner', value: owner.value ? userLabel(owner.value) : item.ownerUserId, mono: true },
    { label: 'Server', value: server.value ? serverLabel(server.value) : item.serverId },
    {
      label: 'Size',
      value: item.sizeObservedAt
        ? `${formatMeasuredBytes(item.sizeBytes)} · measured ${formatDateTime(item.sizeObservedAt)}`
        : formatMeasuredBytes(item.sizeBytes),
    },
    { label: 'Created', value: formatDateTime(item.createdAt) },
  ]
})

// --- Grant access (inline form, FastPanel's per-database user management) ---

const grantUserId = ref('')
const access = ref<AccessLevel>('read_write')
const grantAttempted = ref(false)

const grantUserError = computed(() => (grantAttempted.value && !grantUserId.value ? 'Select a user.' : ''))

/** Users on this server that do not already own or hold a grant on it. */
const grantableUsers = computed(() =>
  data.activeUsers.value.filter(
    (user) =>
      user.serverId === database.value?.serverId &&
      user.id !== database.value?.ownerUserId &&
      !grants.value.some((grant) => grant.userId === user.id),
  ),
)

async function submitGrant() {
  if (!canWrite.value || grantRunner.busy.value) return
  grantAttempted.value = true
  if (grantUserError.value) return
  const label = data.userName(grantUserId.value)
  await grantRunner.run(
    async () =>
      (await createGrant({ databaseId: databaseId.value, userId: grantUserId.value, access: access.value })).job.id,
    {
      onSettled: refreshAll,
      successToast: `Granted ${label} access`,
      failureMessage: 'Granting access failed',
    },
  )
  if (!grantRunner.error.value) {
    grantUserId.value = ''
    access.value = 'read_write'
    grantAttempted.value = false
  }
}

const dropGrantTarget = ref<{ id: string; label: string }>()

function confirmDropGrant() {
  const grant = dropGrantTarget.value
  if (!grant || !canWrite.value) return
  dropGrantTarget.value = undefined
  void dropRunner.run(async () => (await dropGrant(grant.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Revoked ${grant.label}`,
    failureMessage: 'Revoking access failed',
  })
}

// --- Backups and restore ---

const restorePendingId = ref<string>()

watch(restoreRunner.busy, (busy) => {
  if (!busy) restorePendingId.value = undefined
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

const restoreTarget = ref<RestorePoint>()

function confirmRestore() {
  const point = restoreTarget.value
  if (!point || !canWrite.value) return
  restoreTarget.value = undefined
  restorePendingId.value = point.id
  void restoreRunner.run(async () => (await restoreBackup(point.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Restored ${database.value?.name ?? 'database'}`,
    failureMessage: 'The restore failed',
  })
}

// --- Delete database ---

const dropDatabaseOpen = ref(false)

function confirmDropDatabase() {
  const item = database.value
  if (!item || !canWrite.value) return
  dropDatabaseOpen.value = false
  void dropRunner.run(async () => (await dropDatabase(item.id)).job.id, {
    onSettled: refreshAll,
    successToast: `Deleted ${item.name}`,
    failureMessage: 'Deleting the database failed',
  })
}

// After a successful delete the database disappears from the list; leave the
// orphaned detail page for the table it came from.
watch(missing, (gone) => {
  if (gone && dropRunner.progress.value) void router.replace('/databases')
})
</script>

<template>
  <section class="space-y-6">
    <div v-if="loading" class="space-y-1">
      <SkeletonRow v-for="index in 4" :key="index" />
    </div>

    <AppAlert v-else-if="data.databasesQuery.isError.value" tone="danger">
      <div class="flex flex-wrap items-center gap-3">
        <span class="min-w-0 flex-1">This database could not be loaded.</span>
        <AppButton size="sm" @click="data.databasesQuery.refetch()">Retry</AppButton>
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
          <AppButton icon="arrow-left">Back to databases</AppButton>
        </RouterLink>
      </template>
    </EmptyState>

    <template v-else-if="database">
      <PageHeader
        :breadcrumbs="[{ label: 'Databases', to: '/databases' }, { label: database.name }]"
        :title="database.name"
      >
        <StatusPill :status="database.status" />
        <AppButton
          v-if="database.status === 'active' && canLaunch"
          icon="external-link"
          :loading="toolLaunch.launchingId.value === database.id"
          :disabled="toolLaunch.availability(ENGINES[database.engine].tool) !== 'ready'"
          @click="openWebClient"
        >
          Open {{ ENGINES[database.engine].toolLabel }}
        </AppButton>
        <AppButton v-if="canConnect" icon="plug" @click="showConnection = !showConnection">Connect</AppButton>
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

      <div v-if="dropRunner.error.value || dropRunner.progress.value" class="space-y-2">
        <JobFailureNotice v-if="dropRunner.error.value" v-bind="dropRunner.failureProps.value" />
        <JobProgress
          v-if="dropRunner.progress.value"
          :event="dropRunner.progress.value"
          v-bind="dropRunner.progressProps.value"
        />
      </div>
      <AppAlert v-if="toolLaunch.error.value" tone="danger">{{ toolLaunch.error.value }}</AppAlert>

      <AppCard eyebrow="Overview" :title="database.name">
        <FactList :facts="overviewFacts" />
        <AppAlert v-if="database.failure" tone="danger" class="mt-4">{{ database.failure }}</AppAlert>
      </AppCard>

      <!-- Remote connection: an expandable section, not a floating dialog. -->
      <AppCard v-if="canConnect && showConnection" eyebrow="Remote connection" title="Connect">
        <ConnectionDetails
          v-if="server"
          :engine="connectionEngine(server)"
          :engine-label="serverLabel(server)"
          :port="server.port"
          :database="database.name"
          :users="connectionUsers"
          :socket-path="server.socketPath"
        />
      </AppCard>

      <!-- Access: the owner plus every granted user, which is what FastPanel
           calls the database's users. -->
      <AppCard flush eyebrow="Who can reach this database" title="Access">
        <div class="space-y-4 px-3 pb-3 sm:px-4 sm:pb-4">
          <EmptyState
            v-if="!accessRows.length"
            icon="users"
            title="No access yet"
            description="Grant a user connect, read-only, or read-and-write access to this database."
          />
          <div v-else class="overflow-x-auto">
            <table class="w-full border-collapse text-left">
              <thead>
                <tr class="border-b border-outline">
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">User</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Access</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Created</th>
                  <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                  <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody class="divide-y divide-outline">
                <tr v-for="row in accessRows" :key="row.user.id">
                  <td class="px-3 py-2.5 font-mono text-[13px] font-medium whitespace-nowrap text-ink">
                    {{ userLabel(row.user) }}
                  </td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">{{ row.access }}</td>
                  <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                    {{ formatDateTime(row.user.createdAt) }}
                  </td>
                  <td class="px-3 py-2.5"><StatusPill :status="row.grantStatus ?? row.user.status" /></td>
                  <td class="px-3 py-2.5 text-right">
                    <span class="flex items-center justify-end gap-1">
                      <RouterLink
                        v-if="canWrite && row.user.status === 'active'"
                        :to="`/databases/users/${encodeURIComponent(row.user.id)}/password`"
                      >
                        <AppButton size="sm" icon="key">Change password</AppButton>
                      </RouterLink>
                      <AppButton
                        v-if="canWrite && row.grantId && (row.grantStatus === 'active' || row.grantStatus === 'failed')"
                        size="sm"
                        variant="ghost"
                        icon="trash"
                        :aria-label="`Revoke ${userLabel(row.user)}`"
                        :disabled="dropRunner.busy.value"
                        @click="dropGrantTarget = { id: row.grantId, label: `${userLabel(row.user)} → ${database.name}` }"
                      />
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- Inline grant form: pick a user, pick a level, done. -->
          <form
            v-if="canWrite && database.status === 'active'"
            class="flex flex-wrap items-end gap-3 rounded-xl border border-outline bg-surface/60 p-3"
            novalidate
            @submit.prevent="submitGrant"
          >
            <FormField label="Grant access to" class="min-w-56 flex-1" :error="grantUserError">
              <Combobox v-model="grantUserId" :disabled="grantRunner.busy.value">
                <ComboboxAnchor as-child>
                  <ComboboxTrigger
                    :invalid="!!grantUserError"
                    placeholder="Select user"
                    :label="((id) => {
                      const user = grantableUsers.find((item) => item.id === id)
                      return user ? userLabel(user) : ''
                    })(grantUserId)"
                  />
                </ComboboxAnchor>
                <ComboboxList>
                  <ComboboxInput placeholder="Search users…" />
                  <ComboboxEmpty>Every active user on this server already has access</ComboboxEmpty>
                  <ComboboxGroup>
                    <ComboboxItem
                      v-for="user in grantableUsers"
                      :key="user.id"
                      :value="user.id"
                      :text-value="userLabel(user)"
                    >
                      {{ userLabel(user) }}<ComboboxItemIndicator />
                    </ComboboxItem>
                  </ComboboxGroup>
                </ComboboxList>
              </Combobox>
            </FormField>
            <FormField label="Access" class="w-44">
              <Select v-model="access" :disabled="grantRunner.busy.value">
                <SelectTrigger placeholder="Select access" />
                <SelectContent>
                  <SelectItem value="connect">Connect only</SelectItem>
                  <SelectItem value="read_only">Read only</SelectItem>
                  <SelectItem value="read_write">Read and write</SelectItem>
                </SelectContent>
              </Select>
            </FormField>
            <AppButton variant="primary" type="submit" icon="plus" :loading="grantRunner.busy.value">
              Grant access
            </AppButton>
          </form>
          <JobFailureNotice v-if="grantRunner.error.value" v-bind="grantRunner.failureProps.value" />
          <JobProgress
            v-if="grantRunner.progress.value"
            :event="grantRunner.progress.value"
            v-bind="grantRunner.progressProps.value"
          />
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
            <JobFailureNotice v-if="backupRunner.error.value" v-bind="backupRunner.failureProps.value" />
            <JobProgress
              v-if="backupRunner.progress.value"
              :event="backupRunner.progress.value"
              v-bind="backupRunner.progressProps.value"
            />
            <JobFailureNotice v-if="restoreRunner.error.value" v-bind="restoreRunner.failureProps.value" />
            <JobProgress
              v-if="restoreRunner.progress.value"
              :event="restoreRunner.progress.value"
              v-bind="restoreRunner.progressProps.value"
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
                    <AppButton
                      v-if="canWrite && point.status === 'verified'"
                      size="sm"
                      variant="danger"
                      :loading="restoreRunner.busy.value && restorePendingId === point.id"
                      :disabled="restoreRunner.busy.value"
                      @click="restoreTarget = point"
                    >
                      Restore
                    </AppButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </AppCard>

      <!-- Danger zone -->
      <AppCard v-if="canWrite" eyebrow="Danger zone" title="Delete this database">
        <div class="flex flex-wrap items-center gap-3">
          <p class="min-w-0 flex-1 text-[13px] text-ink-secondary">
            Permanently removes the database and everything in it. Back it up first if you might need the data.
          </p>
          <AppButton
            variant="danger"
            icon="trash"
            :disabled="dropRunner.busy.value || (database.status !== 'active' && database.status !== 'failed')"
            @click="dropDatabaseOpen = true"
          >
            Delete database
          </AppButton>
        </div>
      </AppCard>

      <AppConfirmDialog
        :open="canWrite && !!restoreTarget"
        :title="`Restore ${database.name}?`"
        confirm-label="Restore database"
        tone="danger"
        :type-to-confirm="database.name"
        @confirm="confirmRestore"
        @close="restoreTarget = undefined"
      >
        Restoring replaces the current data and terminates active connections. Anything written since this backup is
        lost.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="canWrite && dropDatabaseOpen"
        :title="`Delete database ${database.name}?`"
        confirm-label="Delete database"
        tone="danger"
        :type-to-confirm="database.name"
        @confirm="confirmDropDatabase"
        @close="dropDatabaseOpen = false"
      >
        This permanently deletes the database and everything in it. There is no undo — back it up first if you might
        need the data.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="canWrite && !!dropGrantTarget"
        :title="dropGrantTarget ? `Revoke ${dropGrantTarget.label}?` : 'Revoke access?'"
        confirm-label="Revoke access"
        tone="danger"
        @confirm="confirmDropGrant"
        @close="dropGrantTarget = undefined"
      >
        The user keeps its login but loses all access to this database.
      </AppConfirmDialog>
    </template>
  </section>
</template>
