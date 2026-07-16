<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { formatBytes, formatTime } from '@/shared/formatters'
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
  createBackup,
  createDatabase,
  createGrant,
  createInstance,
  createRole,
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
} from '../api'

interface SelectedResource {
  type: ResourceType
  id: string
  label: string
}

const instancesQuery = useQuery({ queryKey: ['postgresql-instances'], queryFn: listInstances, retry: false })
const rolesQuery = useQuery({ queryKey: ['postgresql-roles'], queryFn: listRoles, retry: false })
const databasesQuery = useQuery({ queryKey: ['postgresql-databases'], queryFn: listDatabases, retry: false })
const grantsQuery = useQuery({ queryKey: ['postgresql-grants'], queryFn: listGrants, retry: false })
const restorePointsQuery = useQuery({ queryKey: ['postgresql-restore-points'], queryFn: listRestorePoints, retry: false })

const instances = computed(() => instancesQuery.data.value ?? [])
const roles = computed(() => rolesQuery.data.value ?? [])
const databases = computed(() => databasesQuery.data.value ?? [])
const restorePoints = computed(() => restorePointsQuery.data.value ?? [])
const activeInstances = computed(() => instances.value.filter((item) => item.status === 'active' || item.status === 'online'))
const activeRoles = computed(() => roles.value.filter((item) => item.status === 'active'))
const activeDatabases = computed(() => databases.value.filter((item) => item.status === 'active'))

// Form state
const version = ref<'16' | '17' | '18'>('18')
const cluster = ref('nexa_main')
const port = ref<number | undefined>()
const roleInstance = ref('')
const roleName = ref('')
const databaseInstance = ref('')
const databaseName = ref('')
const ownerRoleId = ref('')
const grantDatabaseId = ref('')
const grantRoleId = ref('')
const access = ref<AccessLevel>('read_write')

const credential = ref('')
const selected = ref<SelectedResource>()
const selectedPlan = ref<PostgresPlan>()
const planExpiresAt = ref('')

const runner = useJobRunner()

function roleLabel(id: string) {
  return roles.value.find((role) => role.id === id)?.name ?? id
}

function instanceLabel(id: string) {
  const instance = instances.value.find((item) => item.id === id)
  return instance ? `PostgreSQL ${instance.version} · ${instance.cluster}` : id
}

function databaseLabel(id: string) {
  return databases.value.find((database) => database.id === id)?.name ?? id
}

async function refreshAll() {
  await Promise.all([
    instancesQuery.refetch(),
    rolesQuery.refetch(),
    databasesQuery.refetch(),
    grantsQuery.refetch(),
    restorePointsQuery.refetch(),
  ])
}

async function loadPlan(resource: SelectedResource) {
  selected.value = resource
  selectedPlan.value = undefined
  runner.error.value = ''
  try {
    const response = await getPlan(resource.type, resource.id)
    selectedPlan.value = response.plan
    planExpiresAt.value = response.expiresAt
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The PostgreSQL plan is not ready.'
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
      failureMessage: 'The PostgreSQL operation failed. Inspect Jobs for the durable failure record.',
    },
  )
}

const provision = () =>
  queue(async () => {
    const result = await createInstance({ version: version.value, cluster: cluster.value, ...(port.value ? { port: port.value } : {}) })
    return { jobId: result.job.id, resource: { type: 'instances', id: result.instance.id, label: `PostgreSQL ${version.value} · ${cluster.value}` } }
  })

const addRole = () =>
  queue(async () => {
    const result = await createRole(roleInstance.value, roleName.value)
    return { jobId: result.job.id, resource: { type: 'roles', id: result.role.id, label: result.role.name } }
  })

const addDatabase = () =>
  queue(async () => {
    const result = await createDatabase({ instanceId: databaseInstance.value, name: databaseName.value, ownerRoleId: ownerRoleId.value })
    return { jobId: result.job.id, resource: { type: 'databases', id: result.database.id, label: result.database.name } }
  })

const addGrant = () =>
  queue(async () => {
    const result = await createGrant({ databaseId: grantDatabaseId.value, roleId: grantRoleId.value, access: access.value })
    return { jobId: result.job.id, resource: { type: 'grants', id: result.grant.id, label: `${roleLabel(result.grant.roleId)} · ${result.grant.access}` } }
  })

const backup = (databaseId: string, label: string) =>
  queue(async () => {
    const result = await createBackup(databaseId)
    return { jobId: result.job.id, resource: { type: 'restore-points', id: result.restorePoint.id, label: `Backup · ${label}` } }
  })

const restore = (restorePointId: string, label: string) =>
  queue(async () => {
    const result = await prepareRestore(restorePointId)
    return { jobId: result.job.id, resource: { type: 'restore-points', id: result.restorePoint.id, label: `Restore · ${label}` } }
  })

function rotate(role: DatabaseRole) {
  credential.value = ''
  return queue(async () => {
    const result = await rotateRole(role.id)
    return { jobId: result.job.id, resource: { type: 'roles', id: role.id, label: `Rotate · ${role.name}` } }
  })
}

async function approve() {
  const resource = selected.value
  if (!resource) return
  await runner.run(async () => (await applyPlan(resource.type, resource.id)).id, {
    onSettled: refreshAll,
    failureMessage: 'The PostgreSQL operation failed. Inspect Jobs for the durable failure record.',
  })
}

async function reveal(role: DatabaseRole) {
  runner.error.value = ''
  try {
    credential.value = await revealCredential(role.id)
    await rolesQuery.refetch()
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The credential could not be revealed.'
  }
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Managed data layer"
      title="PostgreSQL"
      description="Every mutation is planned and agent-signed. Passwords are encrypted at rest, omitted from jobs and plans, and available exactly once after creation or rotation."
    >
      <StatusPill tone="accent" label="Versions 16 · 17 · 18" :pulse="false" />
    </PageHeader>

    <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
    <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />
    <CredentialReveal v-if="credential" :credential="credential" @clear="credential = ''" />

    <!-- Builders -->
    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <AppCard eyebrow="Instance" title="Provision cluster">
        <form class="space-y-3" @submit.prevent="provision">
          <FormField label="Version">
            <AppSelect v-model="version">
              <option>16</option>
              <option>17</option>
              <option>18</option>
            </AppSelect>
          </FormField>
          <FormField label="Cluster name">
            <AppInput v-model="cluster" pattern="[a-z][a-z0-9_]+" required />
          </FormField>
          <FormField label="Port" hint="Assigned automatically when empty.">
            <AppInput v-model.number="port" type="number" min="1024" max="65535" />
          </FormField>
          <AppButton variant="primary" type="submit" :disabled="runner.busy.value" class="w-full">
            Prepare instance plan
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="Credentials" title="Create login role">
        <form class="space-y-3" @submit.prevent="addRole">
          <FormField label="Instance">
            <AppSelect v-model="roleInstance" required>
              <option disabled value="">Select instance</option>
              <option v-for="item in activeInstances" :key="item.id" :value="item.id">
                PostgreSQL {{ item.version }} · {{ item.cluster }}
              </option>
            </AppSelect>
          </FormField>
          <FormField label="Role name">
            <AppInput v-model="roleName" pattern="[a-z][a-z0-9_]+" required />
          </FormField>
          <AppButton variant="primary" type="submit" :disabled="runner.busy.value" class="w-full">
            Prepare role plan
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="Logical database" title="Create database">
        <form class="space-y-3" @submit.prevent="addDatabase">
          <FormField label="Instance">
            <AppSelect v-model="databaseInstance" required>
              <option disabled value="">Select instance</option>
              <option v-for="item in activeInstances" :key="item.id" :value="item.id">
                PostgreSQL {{ item.version }} · {{ item.cluster }}
              </option>
            </AppSelect>
          </FormField>
          <FormField label="Database name">
            <AppInput v-model="databaseName" pattern="[a-z][a-z0-9_]+" required />
          </FormField>
          <FormField label="Owner role">
            <AppSelect v-model="ownerRoleId" required>
              <option disabled value="">Select owner</option>
              <option v-for="role in activeRoles.filter((item) => item.instanceId === databaseInstance)" :key="role.id" :value="role.id">
                {{ role.name }}
              </option>
            </AppSelect>
          </FormField>
          <AppButton variant="primary" type="submit" :disabled="runner.busy.value" class="w-full">
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
          <FormField label="Role">
            <AppSelect v-model="grantRoleId" required>
              <option disabled value="">Select role</option>
              <option v-for="role in activeRoles" :key="role.id" :value="role.id">{{ role.name }}</option>
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

    <!-- Instances -->
    <AppCard eyebrow="Observed inventory" :title="`${instances.length} instances · ${databases.length} databases`" flush>
      <div class="px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="instances.length" class="space-y-1">
          <ResourceRow
            v-for="item in instances"
            :key="item.id"
            :title="item.cluster"
            :subtitle="item.dataPath"
            :avatar="item.version"
            clickable
            @select="loadPlan({ type: 'instances', id: item.id, label: `PostgreSQL ${item.version} · ${item.cluster}` })"
          >
            <template #meta>
              <span class="hidden shrink-0 font-mono text-xs text-ink-muted sm:inline">Port {{ item.port }}</span>
            </template>
            <template #status>
              <StatusPill :status="item.status" />
            </template>
          </ResourceRow>
        </div>
        <EmptyState
          v-else
          icon="database"
          title="No PostgreSQL instances"
          description="Provision a dedicated cluster with its own port, data path, and systemd identity."
          class="m-2"
        />
      </div>
    </AppCard>

    <!-- Roles and databases -->
    <div class="grid gap-4 lg:grid-cols-2">
      <AppCard eyebrow="Login identities" title="Roles" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="roles.length" class="space-y-1">
            <ResourceRow
              v-for="role in roles"
              :key="role.id"
              :title="role.name"
              :subtitle="`${instanceLabel(role.instanceId)} · credential v${role.credentialVersion}`"
              icon="key"
            >
              <template #actions>
                <AppButton
                  v-if="role.status === 'plan_ready'"
                  size="sm"
                  @click="loadPlan({ type: 'roles', id: role.id, label: role.name })"
                >
                  Review
                </AppButton>
                <AppButton v-if="role.credentialAvailable" size="sm" icon="key" @click="reveal(role)">Reveal once</AppButton>
                <AppButton v-if="role.status === 'active'" size="sm" icon="refresh-cw" :disabled="runner.busy.value" @click="rotate(role)">
                  Rotate
                </AppButton>
              </template>
              <template #status>
                <StatusPill :status="role.status" />
              </template>
            </ResourceRow>
          </div>
          <EmptyState v-else icon="key" title="No roles" description="Create a login role to own databases." class="m-2" />
        </div>
      </AppCard>

      <AppCard eyebrow="Owned resources" title="Databases" flush>
        <div class="px-3 pb-3 sm:px-4 sm:pb-4">
          <div v-if="databases.length" class="space-y-1">
            <ResourceRow
              v-for="item in databases"
              :key="item.id"
              :title="item.name"
              :subtitle="`Owner ${roleLabel(item.ownerRoleId)}`"
              icon="database"
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
          <EmptyState v-else icon="database" title="No databases" description="Create a logical database owned by a role." class="m-2" />
        </div>
      </AppCard>
    </div>

    <!-- Restore points -->
    <AppCard eyebrow="Recovery" title="Verified restore points" flush>
      <div class="px-3 pb-3 sm:px-4 sm:pb-4">
        <div v-if="restorePoints.length" class="space-y-1">
          <ResourceRow
            v-for="point in restorePoints"
            :key="point.id"
            :title="databaseLabel(point.databaseId)"
            :subtitle="`${point.verifiedAt ? new Date(point.verifiedAt).toLocaleString() : 'Verification pending'} · ${formatBytes(point.sizeBytes)}`"
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
              <AppButton
                v-if="point.status === 'verified'"
                size="sm"
                variant="danger"
                :disabled="runner.busy.value"
                @click="restore(point.id, databaseLabel(point.databaseId))"
              >
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
          description="Back up an active database to create a verified restore point."
          class="m-2"
        />
      </div>
    </AppCard>

    <!-- Plan review -->
    <AppCard v-if="selected && selectedPlan" eyebrow="Agent-signed plan" :title="selected.label">
      <template #actions>
        <StatusPill tone="warning" :label="`Expires ${formatTime(planExpiresAt)}`" :pulse="false" />
      </template>

      <div class="space-y-4">
        <PlanSteps :steps="selectedPlan.agentPlan.steps" :warnings="selectedPlan.agentPlan.warnings" />
        <AppAlert v-if="selectedPlan.agentPlan.interruption" tone="danger">
          This restore terminates active database connections during the final verified name swap.
        </AppAlert>
        <div class="flex flex-wrap gap-2">
          <AppButton variant="primary" :loading="runner.busy.value" @click="approve">Approve and execute</AppButton>
        </div>
      </div>
    </AppCard>
  </section>
</template>
