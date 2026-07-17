<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { listDatabases as listPostgresDatabases, listRoles as listPostgresRoles } from '@/modules/databases/api'
import { listAccounts as listMySQLAccounts, listDatabases as listMySQLDatabases } from '@/modules/mysql/api'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppIcon,
  AppSelect,
  EmptyState,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  PlanSteps,
  SkeletonCard,
  StatusPill,
} from '@/shared/ui'

import { applyPlan, getPlan, launchTool, listTools, prepareChange, type AdminTool, type AdminToolPlan, type ToolAction, type ToolKind } from '../api'

const toolsQuery = useQuery({ queryKey: ['admin-tools'], queryFn: listTools, retry: false })
const mysqlDatabasesQuery = useQuery({ queryKey: ['mysql-databases'], queryFn: listMySQLDatabases, retry: false })
const mysqlAccountsQuery = useQuery({ queryKey: ['mysql-accounts'], queryFn: listMySQLAccounts, retry: false })
const postgresDatabasesQuery = useQuery({ queryKey: ['postgresql-databases'], queryFn: listPostgresDatabases, retry: false })
const postgresRolesQuery = useQuery({ queryKey: ['postgresql-roles'], queryFn: listPostgresRoles, retry: false })

const tools = computed(() => toolsQuery.data.value ?? [])
const activeToolKinds = computed(() => new Set(tools.value.filter((tool) => tool.status === 'active').map((tool) => tool.kind)))
const mysqlDatabases = computed(() => (mysqlDatabasesQuery.data.value ?? []).filter((item) => item.status === 'active'))
const mysqlAccounts = computed(() => (mysqlAccountsQuery.data.value ?? []).filter((item) => item.status === 'active'))
const postgresDatabases = computed(() => (postgresDatabasesQuery.data.value ?? []).filter((item) => item.status === 'active'))
const postgresRoles = computed(() => (postgresRolesQuery.data.value ?? []).filter((item) => item.status === 'active'))

const mysqlDatabaseId = ref('')
const mysqlAccountId = ref('')
const postgresDatabaseId = ref('')
const postgresRoleId = ref('')

const selected = ref<AdminTool>()
const selectedPlan = ref<AdminToolPlan>()
const launching = ref(false)
const planLoadError = ref('')
const launchError = ref('')
const blockedLaunch = ref<{ kind: ToolKind; url: string }>()

const runner = useJobRunner()

// Spread-bound so the optional props are omitted (not passed as undefined),
// which exactOptionalPropertyTypes requires.
const runnerJobLink = computed(() => (runner.jobId.value === undefined ? {} : { jobId: runner.jobId.value }))
const runnerTiming = computed(() => (runner.startedAtMs.value === undefined ? {} : { startedAtMs: runner.startedAtMs.value }))

const toolTitles: Record<ToolKind, string> = { phpmyadmin: 'phpMyAdmin', pgadmin: 'pgAdmin' }

async function loadPlan(tool: AdminTool) {
  selected.value = tool
  selectedPlan.value = undefined
  planLoadError.value = ''
  try {
    selectedPlan.value = (await getPlan(tool.kind)).plan
  } catch (caught) {
    planLoadError.value = caught instanceof Error ? caught.message : 'The plan is not ready yet.'
  }
}

async function change(tool: AdminTool, action: ToolAction) {
  await runner.run(
    async () => {
      const result = await prepareChange(tool.kind, action)
      selected.value = result.tool
      return result.job.id
    },
    {
      onSettled: async () => {
        await toolsQuery.refetch()
      },
      onSuccess: async () => {
        if (selected.value) await loadPlan(selected.value)
      },
      failureMessage: 'The admin tool operation failed',
    },
  )
}

async function approve() {
  const tool = selected.value
  if (!tool) return
  await runner.run(async () => (await applyPlan(tool.kind)).id, {
    onSettled: async () => {
      selectedPlan.value = undefined
      await toolsQuery.refetch()
    },
    failureMessage: 'The admin tool operation failed',
  })
}

async function launch(kind: ToolKind) {
  launching.value = true
  launchError.value = ''
  blockedLaunch.value = undefined
  try {
    const input =
      kind === 'phpmyadmin'
        ? { sourceEngine: 'mysql' as const, databaseId: mysqlDatabaseId.value, accountId: mysqlAccountId.value }
        : { sourceEngine: 'postgresql' as const, databaseId: postgresDatabaseId.value, accountId: postgresRoleId.value }
    const response = await launchTool(kind, input)
    // window.open returns null when the feature string contains `noopener`,
    // so sever the opener by hand to keep popup-blocker detection working.
    const opened = window.open(response.url, '_blank')
    if (opened) opened.opener = null
    else blockedLaunch.value = { kind, url: response.url }
  } catch (caught) {
    launchError.value = caught instanceof Error ? caught.message : `${toolTitles[kind]} could not be opened.`
  } finally {
    launching.value = false
  }
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Isolated utilities"
      title="Database admin tools"
      description="phpMyAdmin and pgAdmin run as hardened Podman containers bound to localhost. Launch credentials stay server-side; the browser only receives a short-lived HttpOnly session."
    >
      <StatusPill tone="accent" label="Podman · localhost only" :pulse="false" />
    </PageHeader>

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" v-bind="runnerJobLink" />
    <JobProgress
      v-if="runner.progress.value"
      :event="runner.progress.value"
      :messages="runner.messages.value"
      v-bind="runnerTiming"
    />

    <!-- Tool lifecycle -->
    <div v-if="toolsQuery.isPending.value" class="grid gap-4 sm:grid-cols-2">
      <SkeletonCard v-for="n in 2" :key="n" />
    </div>
    <AppAlert v-else-if="toolsQuery.isError.value" tone="danger">
      <p>The admin tools list couldn't be loaded.</p>
      <AppButton size="sm" class="mt-2" @click="toolsQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!tools.length"
      icon="database"
      title="No admin tools available"
      description="No database admin tools are configured on this host."
    />
    <div v-else class="grid gap-4 sm:grid-cols-2">
      <AppCard
        v-for="tool in tools"
        :key="tool.kind"
        :eyebrow="tool.image"
        :title="toolTitles[tool.kind] ?? tool.kind"
      >
        <template #actions>
          <StatusPill :status="tool.status" />
        </template>
        <div class="space-y-4">
          <dl class="grid grid-cols-3 gap-3 text-center">
            <div class="rounded-xl border border-outline bg-canvas/40 px-2 py-2.5">
              <dt class="text-[10px] font-bold tracking-[0.1em] text-ink-muted uppercase">Memory</dt>
              <dd class="mt-0.5 text-sm font-semibold text-ink">{{ tool.memoryMb }} MiB</dd>
            </div>
            <div class="rounded-xl border border-outline bg-canvas/40 px-2 py-2.5">
              <dt class="text-[10px] font-bold tracking-[0.1em] text-ink-muted uppercase">PIDs</dt>
              <dd class="mt-0.5 text-sm font-semibold text-ink">{{ tool.pidsLimit }}</dd>
            </div>
            <div class="rounded-xl border border-outline bg-canvas/40 px-2 py-2.5">
              <dt class="text-[10px] font-bold tracking-[0.1em] text-ink-muted uppercase">Port</dt>
              <dd class="mt-0.5 font-mono text-sm font-semibold text-ink">{{ tool.port }}</dd>
            </div>
          </dl>
          <p class="text-xs text-ink-muted">
            Container limits are ceilings, not reserved RAM. Bound to <code class="font-mono">127.0.0.1:{{ tool.port }}</code>.
          </p>
          <div class="flex flex-wrap gap-2">
            <AppButton
              v-if="tool.status === 'stopped' || tool.status === 'inactive'"
              variant="primary"
              icon="play"
              :disabled="runner.busy.value"
              @click="change(tool, 'tool.deploy')"
            >
              Deploy and start
            </AppButton>
            <AppButton v-if="tool.status === 'active'" icon="stop" :disabled="runner.busy.value" @click="change(tool, 'tool.stop')">
              Stop
            </AppButton>
            <AppButton v-if="tool.status === 'plan_ready'" :disabled="runner.busy.value" @click="loadPlan(tool)">
              Review plan
            </AppButton>
          </div>
        </div>
      </AppCard>
    </div>

    <AppAlert v-if="launchError" tone="danger">{{ launchError }}</AppAlert>
    <AppAlert v-if="blockedLaunch" tone="info">
      <p>The browser blocked the new tab.</p>
      <a
        :href="blockedLaunch.url"
        target="_blank"
        rel="noopener"
        class="mt-1 inline-flex items-center gap-1.5 font-medium underline underline-offset-2 hover:text-sky-100"
      >
        Open {{ toolTitles[blockedLaunch.kind] }}
        <AppIcon name="external-link" :size="14" />
      </a>
    </AppAlert>

    <!-- Secure launch -->
    <div class="grid gap-4 sm:grid-cols-2">
      <AppCard eyebrow="MySQL-family" title="Open phpMyAdmin">
        <form class="space-y-3" @submit.prevent="launch('phpmyadmin')">
          <FormField label="Database">
            <AppSelect v-model="mysqlDatabaseId" required empty-message="No MySQL databases yet — create one in MySQL / MariaDB">
              <template v-if="mysqlDatabases.length">
                <option disabled value="">Select database</option>
                <option v-for="item in mysqlDatabases" :key="item.id" :value="item.id">{{ item.name }}</option>
              </template>
            </AppSelect>
          </FormField>
          <FormField label="Account">
            <AppSelect v-model="mysqlAccountId" required empty-message="No MySQL accounts yet — create one in MySQL / MariaDB">
              <template v-if="mysqlAccounts.length">
                <option disabled value="">Select account</option>
                <option v-for="item in mysqlAccounts" :key="item.id" :value="item.id">{{ item.name }}@{{ item.host }}</option>
              </template>
            </AppSelect>
          </FormField>
          <AppButton
            variant="primary"
            type="submit"
            icon="external-link"
            :loading="launching"
            :disabled="!activeToolKinds.has('phpmyadmin')"
            class="w-full"
          >
            Launch phpMyAdmin
          </AppButton>
          <p v-if="!toolsQuery.isPending.value && !activeToolKinds.has('phpmyadmin')" class="text-xs text-ink-muted">
            phpMyAdmin is not running — deploy it above first.
          </p>
        </form>
      </AppCard>

      <AppCard eyebrow="PostgreSQL" title="Open pgAdmin">
        <form class="space-y-3" @submit.prevent="launch('pgadmin')">
          <FormField label="Database">
            <AppSelect v-model="postgresDatabaseId" required empty-message="No PostgreSQL databases yet — create one in Databases">
              <template v-if="postgresDatabases.length">
                <option disabled value="">Select database</option>
                <option v-for="item in postgresDatabases" :key="item.id" :value="item.id">{{ item.name }}</option>
              </template>
            </AppSelect>
          </FormField>
          <FormField label="Role">
            <AppSelect v-model="postgresRoleId" required empty-message="No PostgreSQL roles yet — create one in Databases">
              <template v-if="postgresRoles.length">
                <option disabled value="">Select role</option>
                <option v-for="item in postgresRoles" :key="item.id" :value="item.id">{{ item.name }}</option>
              </template>
            </AppSelect>
          </FormField>
          <AppButton
            variant="primary"
            type="submit"
            icon="external-link"
            :loading="launching"
            :disabled="!activeToolKinds.has('pgadmin')"
            class="w-full"
          >
            Launch pgAdmin
          </AppButton>
          <p v-if="!toolsQuery.isPending.value && !activeToolKinds.has('pgadmin')" class="text-xs text-ink-muted">
            pgAdmin is not running — deploy it above first.
          </p>
        </form>
      </AppCard>
    </div>

    <!-- Plan review -->
    <AppAlert v-if="planLoadError" tone="danger">{{ planLoadError }}</AppAlert>
    <AppCard v-if="selected && selectedPlan" eyebrow="Agent-signed Podman plan" :title="toolTitles[selected.kind] ?? selected.kind">
      <div class="space-y-4">
        <PlanSteps :steps="selectedPlan.agentPlan.steps" :warnings="selectedPlan.agentPlan.warnings" />
        <div class="flex flex-wrap gap-2">
          <AppButton variant="primary" :loading="runner.busy.value" @click="approve">Approve and apply</AppButton>
        </div>
      </div>
    </AppCard>
  </section>
</template>
