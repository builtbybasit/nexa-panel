<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppInput,
  AppSelect,
  CredentialReveal,
  EmptyState,
  FormField,
  JobProgress,
  PageHeader,
  PlanSteps,
  ResourceRow,
  StatusPill,
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
} from '../api'

interface SelectedResource {
  type: ResourceType
  id: string
  label: string
}

const enginesQuery = useQuery({ queryKey: ['mysql-engines'], queryFn: listEngines, retry: false })
const accountsQuery = useQuery({ queryKey: ['mysql-accounts'], queryFn: listAccounts, retry: false })
const databasesQuery = useQuery({ queryKey: ['mysql-databases'], queryFn: listDatabases, retry: false })
const grantsQuery = useQuery({ queryKey: ['mysql-grants'], queryFn: listGrants, retry: false })
const restorePointsQuery = useQuery({ queryKey: ['mysql-restore-points'], queryFn: listRestorePoints, retry: false })

const engines = computed(() => enginesQuery.data.value ?? [])
const engine = computed(() => engines.value[0])
const accounts = computed(() => accountsQuery.data.value ?? [])
const databases = computed(() => databasesQuery.data.value ?? [])
const restorePoints = computed(() => restorePointsQuery.data.value ?? [])
const activeAccounts = computed(() => accounts.value.filter((item) => item.status === 'active'))
const activeDatabases = computed(() => databases.value.filter((item) => item.status === 'active'))

// Form state
const accountName = ref('')
const accountHost = ref('localhost')
const databaseName = ref('')
const ownerAccountId = ref('')
const grantDatabaseId = ref('')
const grantAccountId = ref('')
const access = ref<Access>('read_write')

const credential = ref('')
const selected = ref<SelectedResource>()
const selectedPlan = ref<Plan>()

const runner = useJobRunner()

function databaseLabel(id: string) {
  return databases.value.find((database) => database.id === id)?.name ?? id
}

async function refreshAll() {
  await Promise.all([
    enginesQuery.refetch(),
    accountsQuery.refetch(),
    databasesQuery.refetch(),
    grantsQuery.refetch(),
    restorePointsQuery.refetch(),
  ])
}

async function loadPlan(resource: SelectedResource) {
  selected.value = resource
  selectedPlan.value = undefined
  try {
    selectedPlan.value = (await getPlan(resource.type, resource.id)).plan
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The MySQL-family plan is not ready.'
  }
}

async function queue(action: () => Promise<{ jobId: number; resource: SelectedResource }>, loadAfter = true) {
  let resource: SelectedResource | undefined
  await runner.run(
    async () => {
      const result = await action()
      resource = result.resource
      return result.jobId
    },
    {
      onSettled: refreshAll,
      onSuccess: async () => {
        if (loadAfter && resource) await loadPlan(resource)
      },
      failureMessage: 'The MySQL-family operation failed. Inspect Jobs for the durable failure record.',
    },
  )
}

const addAccount = () =>
  queue(async () => {
    const activeEngine = engine.value
    if (!activeEngine) throw new Error('No active native engine was discovered.')
    const result = await createAccount({ engineId: activeEngine.id, name: accountName.value, host: accountHost.value })
    return { jobId: result.job.id, resource: { type: 'accounts', id: result.account.id, label: result.account.name } }
  })

const addDatabase = () =>
  queue(async () => {
    const activeEngine = engine.value
    if (!activeEngine) throw new Error('No active native engine was discovered.')
    const result = await createDatabase({ engineId: activeEngine.id, name: databaseName.value, ownerAccountId: ownerAccountId.value })
    return { jobId: result.job.id, resource: { type: 'databases', id: result.database.id, label: result.database.name } }
  })

const addGrant = () =>
  queue(async () => {
    const result = await createGrant({ databaseId: grantDatabaseId.value, accountId: grantAccountId.value, access: access.value })
    return { jobId: result.job.id, resource: { type: 'grants', id: result.grant.id, label: 'Database grant' } }
  })

const backup = (databaseId: string, name: string) =>
  queue(async () => {
    const result = await createBackup(databaseId)
    return { jobId: result.job.id, resource: { type: 'restore-points', id: result.restorePoint.id, label: `Backup · ${name}` } }
  })

const restore = (restorePointId: string) =>
  queue(async () => {
    const result = await prepareRestore(restorePointId)
    return { jobId: result.job.id, resource: { type: 'restore-points', id: result.restorePoint.id, label: 'Restore database' } }
  })

function rotate(account: Account) {
  credential.value = ''
  return queue(async () => {
    const result = await rotateAccount(account.id)
    return { jobId: result.job.id, resource: { type: 'accounts', id: account.id, label: `Rotate · ${account.name}` } }
  })
}

async function approve() {
  const resource = selected.value
  if (!resource) return
  await runner.run(async () => (await applyPlan(resource.type, resource.id)).id, {
    onSettled: refreshAll,
    failureMessage: 'The MySQL-family operation failed. Inspect Jobs for the durable failure record.',
  })
}

async function reveal(account: Account) {
  runner.error.value = ''
  try {
    credential.value = await revealCredential(account.id)
    await accountsQuery.refetch()
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The credential could not be revealed.'
  }
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="One native engine"
      title="MySQL & MariaDB"
      description="Nexa discovers exactly one active native engine per host. Credential mutations stop while the server general query log is enabled."
    >
      <StatusPill v-if="engine" tone="success" :label="`${engine.kind} ${engine.version}`" :pulse="false" />
    </PageHeader>

    <AppAlert v-if="enginesQuery.isSuccess.value && !engine" tone="warning">
      No active native MySQL or MariaDB server was discovered through its local Unix socket.
    </AppAlert>
    <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
    <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />
    <CredentialReveal v-if="credential" :credential="credential" @clear="credential = ''" />

    <!-- Builders -->
    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      <AppCard eyebrow="Credentials" title="Create account">
        <form class="space-y-3" @submit.prevent="addAccount">
          <FormField label="Name">
            <AppInput v-model="accountName" pattern="[a-z][a-z0-9_]+" required />
          </FormField>
          <FormField label="Host">
            <AppInput v-model="accountHost" required />
          </FormField>
          <AppButton variant="primary" type="submit" :disabled="runner.busy.value || !engine" class="w-full">
            Prepare account plan
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="Logical database" title="Create database">
        <form class="space-y-3" @submit.prevent="addDatabase">
          <FormField label="Name">
            <AppInput v-model="databaseName" pattern="[a-z][a-z0-9_]+" required />
          </FormField>
          <FormField label="Primary account">
            <AppSelect v-model="ownerAccountId" required>
              <option disabled value="">Select account</option>
              <option v-for="item in activeAccounts" :key="item.id" :value="item.id">{{ item.name }}@{{ item.host }}</option>
            </AppSelect>
          </FormField>
          <AppButton variant="primary" type="submit" :disabled="runner.busy.value || !engine" class="w-full">
            Prepare database plan
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="Scoped permissions" title="Assign access">
        <form class="space-y-3" @submit.prevent="addGrant">
          <FormField label="Database">
            <AppSelect v-model="grantDatabaseId" required>
              <option disabled value="">Select database</option>
              <option v-for="item in activeDatabases" :key="item.id" :value="item.id">{{ item.name }}</option>
            </AppSelect>
          </FormField>
          <FormField label="Account">
            <AppSelect v-model="grantAccountId" required>
              <option disabled value="">Select account</option>
              <option v-for="item in activeAccounts" :key="item.id" :value="item.id">{{ item.name }}@{{ item.host }}</option>
            </AppSelect>
          </FormField>
          <FormField label="Access">
            <AppSelect v-model="access">
              <option value="connect">Connect only</option>
              <option value="read_only">Read only</option>
              <option value="read_write">Read and write</option>
            </AppSelect>
          </FormField>
          <AppButton variant="primary" type="submit" :disabled="runner.busy.value" class="w-full">
            Prepare grant plan
          </AppButton>
        </form>
      </AppCard>
    </div>

    <!-- Accounts and databases -->
    <div class="grid gap-4 lg:grid-cols-2">
      <AppCard eyebrow="Login identities" title="Accounts" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="accounts.length" class="space-y-1">
            <ResourceRow
              v-for="item in accounts"
              :key="item.id"
              :title="`${item.name}@${item.host}`"
              :subtitle="`Credential v${item.credentialVersion}`"
              icon="key"
            >
              <template #actions>
                <AppButton
                  v-if="item.status === 'plan_ready'"
                  size="sm"
                  @click="loadPlan({ type: 'accounts', id: item.id, label: item.name })"
                >
                  Review
                </AppButton>
                <AppButton v-if="item.credentialAvailable" size="sm" icon="key" @click="reveal(item)">Reveal once</AppButton>
                <AppButton v-if="item.status === 'active'" size="sm" icon="refresh-cw" :disabled="runner.busy.value" @click="rotate(item)">
                  Rotate
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="item.status" />
              </template>
            </ResourceRow>
          </div>
          <EmptyState v-else icon="key" title="No accounts" description="Create a scoped database account." class="m-2" />
        </div>
      </AppCard>

      <AppCard eyebrow="Owned resources" title="Databases" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="databases.length" class="space-y-1">
            <ResourceRow
              v-for="item in databases"
              :key="item.id"
              :title="item.name"
              :subtitle="engine ? `${engine.kind} ${engine.version}` : 'Native engine'"
              icon="server"
            >
              <template #actions>
                <AppButton
                  v-if="item.status === 'plan_ready'"
                  size="sm"
                  @click="loadPlan({ type: 'databases', id: item.id, label: item.name })"
                >
                  Review
                </AppButton>
                <AppButton v-if="item.status === 'active'" size="sm" icon="copy" :disabled="runner.busy.value" @click="backup(item.id, item.name)">
                  Back up
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="item.status" />
              </template>
            </ResourceRow>
          </div>
          <EmptyState v-else icon="server" title="No databases" description="Create a database owned by an account." class="m-2" />
        </div>
      </AppCard>
    </div>

    <!-- Restore points -->
    <AppCard eyebrow="Recovery" title="Restore points" flush>
      <div class="px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="restorePoints.length" class="space-y-1">
          <ResourceRow
            v-for="point in restorePoints"
            :key="point.id"
            :title="databaseLabel(point.databaseId)"
            :subtitle="formatBytes(point.sizeBytes)"
            icon="rotate-ccw"
          >
            <template #actions>
              <AppButton
                v-if="point.status === 'plan_ready'"
                size="sm"
                @click="loadPlan({ type: 'restore-points', id: point.id, label: 'Restore point' })"
              >
                Review
              </AppButton>
              <AppButton v-if="point.status === 'verified'" size="sm" variant="danger" :disabled="runner.busy.value" @click="restore(point.id)">
                Prepare restore
              </AppButton>
            </template>
            <template #status>
              <StatusPill :status="point.status" />
            </template>
          </ResourceRow>
        </div>
        <EmptyState
          v-else
          icon="rotate-ccw"
          title="No restore points"
          description="Back up an active database to create a restore point with a rollback dump."
          class="m-2"
        />
      </div>
    </AppCard>

    <!-- Plan review -->
    <AppCard v-if="selected && selectedPlan" eyebrow="Agent-signed plan" :title="selected.label">
      <div class="space-y-4">
        <PlanSteps :steps="selectedPlan.agentPlan.steps" :warnings="selectedPlan.agentPlan.warnings" />
        <AppAlert v-if="selectedPlan.agentPlan.interruption" tone="danger">
          This operation interrupts active connections while it completes.
        </AppAlert>
        <div class="flex flex-wrap gap-2">
          <AppButton variant="primary" :loading="runner.busy.value" @click="approve">Approve and execute</AppButton>
        </div>
      </div>
    </AppCard>
  </section>
</template>
