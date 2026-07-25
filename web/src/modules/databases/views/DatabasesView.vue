<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import { formatDateTime, formatMeasuredBytes } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppIcon,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  EmptyState,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  SkeletonRow,
  StatusPill,
  TablePager,
} from '@/shared/ui'

import { useToolLaunch } from '@/modules/admintools/composables/useToolLaunch'
import { useIdentityStore } from '@/modules/identity/store'

import { createBackup, dropDatabase, dropUser, type DatabaseUser, type ManagedDatabase } from '../api'
import { useDatabasesData } from '../composables/useDatabasesData'
import { ENGINES, serverLabel, userLabel } from '../lib/engines'

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))
const canLaunch = computed(() => identity.can('operations.apply'))

const data = useDatabasesData()
const { servers, users, databases } = data

// One runner per operation so only the button that launched a job shows busy
// while everything else stays usable.
const backupRunner = useJobRunner()
const dropRunner = useJobRunner()

// One-click phpMyAdmin/pgAdmin launch, logged in as the database's owner.
const toolLaunch = useToolLaunch()

function openWebClient(database: ManagedDatabase) {
  if (!canLaunch.value) return
  void toolLaunch.launch(ENGINES[database.engine].tool, database.engine, database.id)
}

function toolTitle(database: ManagedDatabase): string {
  const engine = ENGINES[database.engine]
  switch (toolLaunch.availability(engine.tool)) {
    case 'ready':
      return `Open ${engine.toolLabel} for ${database.name}`
    case 'loading':
      return `Checking ${engine.toolLabel} status`
    case 'error':
      return `${engine.toolLabel} status is unavailable`
    default:
      return `Install ${engine.toolLabel} from the Applications page to enable this`
  }
}

function detailLink(database: ManagedDatabase) {
  return `/databases/${encodeURIComponent(database.id)}`
}

/** Sizes are measured on read with a refresh interval, so say when, not just what. */
function sizeTitle(database: ManagedDatabase) {
  return database.sizeObservedAt ? `Measured ${formatDateTime(database.sizeObservedAt)}` : undefined
}

const collection = useCollection(() => databases.value, {
  searchText: (item) => `${item.name} ${data.userName(item.ownerUserId)} ${data.serverNameShort(item.serverId)}`,
  pageSize: 10,
})

// --- Row actions (each guarded by a confirm, then executed directly) ---

const backupPendingId = ref<string>()

watch(backupRunner.busy, (busy) => {
  if (!busy) backupPendingId.value = undefined
})

function backupDatabase(database: ManagedDatabase) {
  if (!canWrite.value) return
  backupPendingId.value = database.id
  void backupRunner.run(async () => (await createBackup(database.id)).job.id, {
    onSettled: data.refreshAll,
    successToast: `Backed up ${database.name}`,
    failureMessage: 'The backup failed',
  })
}

const dropDatabaseTarget = ref<ManagedDatabase>()
const dropUserTarget = ref<DatabaseUser>()

function confirmDropDatabase() {
  const database = dropDatabaseTarget.value
  if (!database || !canWrite.value) return
  dropDatabaseTarget.value = undefined
  void dropRunner.run(async () => (await dropDatabase(database.id)).job.id, {
    onSettled: data.refreshAll,
    successToast: `Deleted ${database.name}`,
    failureMessage: 'Deleting the database failed',
  })
}

function confirmDropUser() {
  const user = dropUserTarget.value
  if (!user || !canWrite.value) return
  dropUserTarget.value = undefined
  void dropRunner.run(async () => (await dropUser(user.id)).job.id, {
    onSettled: data.refreshAll,
    successToast: `Deleted ${userLabel(user)}`,
    failureMessage: 'Deleting the user failed',
  })
}

// The old dialog deep-link now lands on the dedicated create page.
watch(
  () => route.query.create,
  (value) => {
    if (value === '1') void router.replace('/databases/new')
  },
  { immediate: true },
)
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Managed data layer"
      title="Databases"
      description="MySQL, MariaDB, and PostgreSQL behind one workflow. Pick a server when you create a database — the engine follows from it."
    >
      <RouterLink v-if="canWrite" to="/databases/servers/new">
        <AppButton icon="plus">Provision instance</AppButton>
      </RouterLink>
      <RouterLink v-if="canWrite" to="/databases/new">
        <AppButton variant="primary" icon="plus">New database</AppButton>
      </RouterLink>
    </PageHeader>

    <AppAlert v-if="!canWrite" tone="info">Your account has read-only access to databases.</AppAlert>

    <div v-if="dropRunner.error.value || dropRunner.progress.value" class="space-y-2">
      <JobFailureNotice v-if="dropRunner.error.value" v-bind="dropRunner.failureProps.value" />
      <JobProgress
        v-if="dropRunner.progress.value"
        :event="dropRunner.progress.value"
        v-bind="dropRunner.progressProps.value"
      />
    </div>

    <!-- Servers: a summary strip rather than a section, because the databases
         table below is what this page is actually for. -->
    <div v-if="data.serversQuery.isPending.value" class="space-y-1">
      <SkeletonRow v-for="index in 2" :key="index" />
    </div>
    <AppAlert v-else-if="data.serversQuery.isError.value" tone="danger">
      <div class="flex flex-wrap items-center gap-3">
        <span class="min-w-0 flex-1">Database servers could not be loaded.</span>
        <AppButton size="sm" @click="data.serversQuery.refetch()">Retry</AppButton>
      </div>
    </AppAlert>
    <EmptyState
      v-else-if="!servers.length"
      icon="database"
      title="No database servers yet"
      description="Install MySQL or MariaDB on this host and it is discovered automatically, or provision a PostgreSQL instance here."
    >
      <template v-if="canWrite" #action>
        <RouterLink to="/databases/servers/new">
          <AppButton icon="plus">Provision instance</AppButton>
        </RouterLink>
      </template>
    </EmptyState>
    <div v-else class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <div
        v-for="item in servers"
        :key="item.id"
        class="flex items-center gap-3 rounded-xl border border-outline bg-surface/80 px-3.5 py-3"
      >
        <span
          class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-accent-300"
        >
          <AppIcon name="database" :size="16" />
        </span>
        <span class="min-w-0 flex-1">
          <strong class="block truncate text-[13px] font-semibold text-ink">{{ serverLabel(item) }}</strong>
          <small class="block truncate font-mono text-[11px] text-ink-muted">Port {{ item.port }}</small>
        </span>
        <StatusPill :status="item.status" />
      </div>
    </div>

    <!-- Databases: the hero. -->
    <AppCard flush eyebrow="Owned resources" title="Databases">
      <template #actions>
        <RouterLink v-if="canWrite" to="/databases/new">
          <AppButton size="sm" icon="plus">New database</AppButton>
        </RouterLink>
      </template>
      <div class="space-y-3 px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="backupRunner.error.value || backupRunner.progress.value" class="space-y-2">
          <JobFailureNotice v-if="backupRunner.error.value" v-bind="backupRunner.failureProps.value" />
          <JobProgress
            v-if="backupRunner.progress.value"
            :event="backupRunner.progress.value"
            v-bind="backupRunner.progressProps.value"
          />
        </div>
        <AppAlert v-if="toolLaunch.toolsQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center gap-3">
            <span class="min-w-0 flex-1">The web client deployment status could not be loaded.</span>
            <AppButton size="sm" @click="toolLaunch.toolsQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <AppAlert v-if="toolLaunch.error.value" tone="danger">{{ toolLaunch.error.value }}</AppAlert>
        <AppAlert v-if="toolLaunch.blocked.value" tone="info">
          <p>The browser blocked the web client tab.</p>
          <a
            :href="toolLaunch.blocked.value.url"
            target="_blank"
            rel="noopener"
            class="mt-1 inline-flex items-center gap-1.5 font-medium underline underline-offset-2"
          >
            Open it manually
            <AppIcon name="external-link" :size="14" />
          </a>
        </AppAlert>
        <div v-if="data.databasesQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="index in 3" :key="index" />
        </div>
        <AppAlert v-else-if="data.databasesQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center gap-3">
            <span class="min-w-0 flex-1">Databases could not be loaded.</span>
            <AppButton size="sm" @click="data.databasesQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <EmptyState
          v-else-if="!databases.length"
          icon="database"
          title="No databases yet"
          description="Create a database on any server — choose the engine as you go."
        >
          <template v-if="canWrite" #action>
            <RouterLink to="/databases/new">
              <AppButton icon="plus">New database</AppButton>
            </RouterLink>
          </template>
        </EmptyState>
        <template v-else>
          <ListToolbar
            v-model:search="collection.search.value"
            :count="collection.matching.value"
            count-label="databases"
            placeholder="Search by name, owner, or server"
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
                    <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Server</th>
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
                      {{ data.serverNameShort(item.serverId) }}
                    </td>
                    <td class="px-3 py-2.5 font-mono text-xs whitespace-nowrap text-ink-secondary">
                      {{ data.userName(item.ownerUserId) }}
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
                          v-if="item.status === 'active' && canLaunch"
                          size="sm"
                          variant="ghost"
                          icon="external-link"
                          :loading="toolLaunch.launchingId.value === item.id"
                          :disabled="toolLaunch.availability(ENGINES[item.engine].tool) !== 'ready'"
                          :aria-label="`Open ${ENGINES[item.engine].toolLabel} for ${item.name}`"
                          :title="toolTitle(item)"
                          @click="openWebClient(item)"
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
                              v-if="canWrite && item.status === 'active'"
                              :disabled="backupRunner.busy.value"
                              @select="backupDatabase(item)"
                            >
                              Back up now
                            </DropdownMenuItem>
                            <DropdownMenuSeparator v-if="canWrite && item.status === 'active'" />
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

    <!-- Users stay on this page rather than moving to the per-database
         drill-down: a database cannot be created without an owner, so users
         reachable only from a database would deadlock the first one. -->
    <AppCard flush eyebrow="Login identities" title="Users">
      <template #actions>
        <RouterLink v-if="canWrite" to="/databases/users/new">
          <AppButton size="sm" icon="plus">Add user</AppButton>
        </RouterLink>
      </template>
      <div class="space-y-3 px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="data.usersQuery.isPending.value" class="space-y-1">
          <SkeletonRow v-for="index in 2" :key="index" />
        </div>
        <AppAlert v-else-if="data.usersQuery.isError.value" tone="danger">
          <div class="flex flex-wrap items-center gap-3">
            <span class="min-w-0 flex-1">Users could not be loaded.</span>
            <AppButton size="sm" @click="data.usersQuery.refetch()">Retry</AppButton>
          </div>
        </AppAlert>
        <EmptyState
          v-else-if="!users.length"
          icon="key"
          title="No users yet"
          description="Users are the login identities that own and access databases. Create one on any active server."
        >
          <template v-if="canWrite" #action>
            <RouterLink to="/databases/users/new">
              <AppButton icon="plus">Add user</AppButton>
            </RouterLink>
          </template>
        </EmptyState>
        <div v-else class="overflow-x-auto">
          <table class="w-full border-collapse text-left">
            <thead>
              <tr class="border-b border-outline">
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">User</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Server</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Created</th>
                <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Status</th>
                <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-outline">
              <tr v-for="user in users" :key="user.id">
                <td class="px-3 py-2.5 font-mono text-[13px] font-medium whitespace-nowrap text-ink">
                  {{ userLabel(user) }}
                </td>
                <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                  {{ data.serverNameShort(user.serverId) }}
                </td>
                <td class="px-3 py-2.5 text-[13px] whitespace-nowrap text-ink-secondary">
                  {{ formatDateTime(user.createdAt) }}
                </td>
                <td class="px-3 py-2.5"><StatusPill :status="user.status" /></td>
                <td class="px-3 py-2.5 text-right">
                  <span class="flex items-center justify-end gap-1">
                    <RouterLink
                      v-if="canWrite && (user.status === 'active' || user.status === 'failed')"
                      :to="`/databases/users/${encodeURIComponent(user.id)}/password`"
                    >
                      <AppButton size="sm" icon="key">Change password</AppButton>
                    </RouterLink>
                    <AppButton
                      v-if="canWrite && (user.status === 'active' || user.status === 'failed')"
                      size="sm"
                      variant="ghost"
                      icon="trash"
                      :aria-label="`Delete ${userLabel(user)}`"
                      :disabled="dropRunner.busy.value"
                      @click="dropUserTarget = user"
                    />
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </AppCard>

    <AppConfirmDialog
      :open="canWrite && !!dropDatabaseTarget"
      :title="dropDatabaseTarget ? `Delete database ${dropDatabaseTarget.name}?` : 'Delete database?'"
      confirm-label="Delete database"
      tone="danger"
      :type-to-confirm="dropDatabaseTarget?.name"
      @confirm="confirmDropDatabase"
      @close="dropDatabaseTarget = undefined"
    >
      This permanently deletes the database and everything in it. There is no undo — back it up first if you might need
      the data.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="canWrite && !!dropUserTarget"
      :title="dropUserTarget ? `Delete user ${userLabel(dropUserTarget)}?` : 'Delete user?'"
      confirm-label="Delete user"
      tone="danger"
      @confirm="confirmDropUser"
      @close="dropUserTarget = undefined"
    >
      Applications signing in as this user will lose access. A database this user owns must keep at least one other
      user, or the deletion is refused; if another user remains, it inherits ownership.
    </AppConfirmDialog>
  </section>
</template>
