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
  AppSelect,
  FormField,
  JobProgress,
  PageHeader,
  PlanSteps,
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

const runner = useJobRunner()

const toolTitles: Record<ToolKind, string> = { phpmyadmin: 'phpMyAdmin', pgadmin: 'pgAdmin' }

async function loadPlan(tool: AdminTool) {
  selected.value = tool
  selectedPlan.value = undefined
  try {
    selectedPlan.value = (await getPlan(tool.kind)).plan
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The admin tool plan is not ready.'
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
      failureMessage: 'The admin tool operation failed. Inspect Jobs for details.',
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
    failureMessage: 'The admin tool operation failed. Inspect Jobs for details.',
  })
}

async function launch(kind: ToolKind) {
  launching.value = true
  runner.error.value = ''
  try {
    const input =
      kind === 'phpmyadmin'
        ? { sourceEngine: 'mysql' as const, databaseId: mysqlDatabaseId.value, accountId: mysqlAccountId.value }
        : { sourceEngine: 'postgresql' as const, databaseId: postgresDatabaseId.value, accountId: postgresRoleId.value }
    const response = await launchTool(kind, input)
    window.location.assign(response.url)
  } catch (caught) {
    runner.error.value = caught instanceof Error ? caught.message : 'The admin tool launch failed.'
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

    <AppAlert v-if="runner.error.value" tone="danger">{{ runner.error.value }}</AppAlert>
    <JobProgress v-if="runner.progress.value" :event="runner.progress.value" />

    <!-- Tool lifecycle -->
    <div class="grid gap-4 sm:grid-cols-2">
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
              Deploy / start
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

    <!-- Secure launch -->
    <div class="grid gap-4 sm:grid-cols-2">
      <AppCard eyebrow="MySQL-family" title="Open phpMyAdmin">
        <form class="space-y-3" @submit.prevent="launch('phpmyadmin')">
          <FormField label="Database">
            <AppSelect v-model="mysqlDatabaseId" required>
              <option disabled value="">Select database</option>
              <option v-for="item in mysqlDatabases" :key="item.id" :value="item.id">{{ item.name }}</option>
            </AppSelect>
          </FormField>
          <FormField label="Account">
            <AppSelect v-model="mysqlAccountId" required>
              <option disabled value="">Select account</option>
              <option v-for="item in mysqlAccounts" :key="item.id" :value="item.id">{{ item.name }}@{{ item.host }}</option>
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
            Launch securely
          </AppButton>
        </form>
      </AppCard>

      <AppCard eyebrow="PostgreSQL" title="Open pgAdmin">
        <form class="space-y-3" @submit.prevent="launch('pgadmin')">
          <FormField label="Database">
            <AppSelect v-model="postgresDatabaseId" required>
              <option disabled value="">Select database</option>
              <option v-for="item in postgresDatabases" :key="item.id" :value="item.id">{{ item.name }}</option>
            </AppSelect>
          </FormField>
          <FormField label="Role">
            <AppSelect v-model="postgresRoleId" required>
              <option disabled value="">Select role</option>
              <option v-for="item in postgresRoles" :key="item.id" :value="item.id">{{ item.name }}</option>
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
            Launch securely
          </AppButton>
        </form>
      </AppCard>
    </div>

    <!-- Plan review -->
    <AppCard v-if="selected && selectedPlan" eyebrow="Agent-signed Podman plan" :title="toolTitles[selected.kind] ?? selected.kind">
      <div class="space-y-4">
        <PlanSteps :steps="selectedPlan.agentPlan.steps" :warnings="selectedPlan.agentPlan.warnings" />
        <div class="flex flex-wrap gap-2">
          <AppButton variant="primary" :loading="runner.busy.value" @click="approve">Approve and execute</AppButton>
        </div>
      </div>
    </AppCard>
  </section>
</template>
