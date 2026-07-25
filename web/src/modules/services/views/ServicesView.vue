<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppConfirmDialog,
  EmptyState,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  SkeletonRow,
  StatusPill,
  Switch,
  TablePager,
} from '@/shared/ui'

import LockoutRevertPanel from '@/modules/safeguard/LockoutRevertPanel.vue'

import {
  confirmServiceRevert,
  getServiceReverts,
  listServices,
  LockoutRiskError,
  serviceAction,
  type Service,
  type ServiceAction,
} from '../api'

const identity = useIdentityStore()
const canWrite = computed(() => identity.can('services.write'))

// A short refetch keeps status and autorun reflecting reality after an action
// without a manual refresh.
const servicesQuery = useQuery({
  queryKey: ['services'],
  queryFn: listServices,
  refetchInterval: 10_000,
  retry: false,
})
const services = computed(() => servicesQuery.data.value ?? [])
const collection = useCollection<Service>(() => services.value, { searchText: (s) => s.name, pageSize: 12 })

// The armed reverts are the server's, not this page's: it polls them so a
// reload, a second browser, or a colleague's session all see the same countdown
// and the same confirm button.
const revertsQuery = useQuery({
  queryKey: ['service-reverts'],
  queryFn: getServiceReverts,
  refetchInterval: 5_000,
  retry: false,
})
const reverts = computed(() => revertsQuery.data.value?.items ?? [])
const confirmingRevert = ref('')

const runner = useJobRunner()
const pendingUnit = ref('')
const criticalStop = ref<Service>()
// The same set the server enforces. It is duplicated here only to ask the first
// question early; the server refuses a stop the browser lets through, so this
// list going stale weakens the copy, never the guard.
const criticalUnits = new Set(['nginx.service', 'nexa-api.service', 'nexa-agent.service', 'ssh.service', 'sshd.service'])
// Set when the server refuses a stop as lockout-capable: it holds the service to
// retry with the acknowledgement, plus the server's own explanation of why.
const lockoutRisk = ref<{ service: Service; message: string }>()

async function run(service: Service, action: ServiceAction, successVerb: string, acknowledgeLockout = false) {
  if (!canWrite.value || runner.busy.value) return
  pendingUnit.value = service.systemdUnit
  await runner.run(
    async () => {
      try {
        const submission = await serviceAction(service.systemdUnit, action, acknowledgeLockout)
        // An accepted critical stop comes back with an armed revert; showing the
        // countdown immediately matters more here than anywhere else on the page.
        await revertsQuery.refetch()
        return submission.job.id
      } catch (caught) {
        // The server refused because the stop takes access with it. That is not
        // a failure to report: it is a second, better-informed question to ask.
        if (caught instanceof LockoutRiskError) {
          lockoutRisk.value = { service, message: caught.message }
          return undefined
        }
        throw caught
      }
    },
    {
      onSettled: async () => {
        await Promise.all([servicesQuery.refetch(), revertsQuery.refetch()])
      },
      successToast: `${service.name} ${successVerb}`,
      failureMessage: `Could not ${action} ${service.name}`,
    },
  )
  pendingUnit.value = ''
}

function toggleAutorun(service: Service, enabled: boolean) {
  run(service, enabled ? 'enable' : 'disable', enabled ? 'enabled on boot' : 'disabled on boot')
}

function requestRun(service: Service, action: ServiceAction, successVerb: string) {
  if (action === 'stop' && criticalUnits.has(service.systemdUnit)) {
    criticalStop.value = service
    return
  }
  void run(service, action, successVerb)
}

function confirmCriticalStop() {
  const service = criticalStop.value
  criticalStop.value = undefined
  if (service) void run(service, 'stop', 'stopped')
}

// The operator has read the server's reasons and still wants the stop. It goes
// through with the acknowledgement set, which is what arms the timed rollback.
function acceptLockoutRisk() {
  const risk = lockoutRisk.value
  lockoutRisk.value = undefined
  if (risk) void run(risk.service, 'stop', 'stopped', true)
}

async function confirmRevert(id: string) {
  confirmingRevert.value = id
  try {
    await confirmServiceRevert(id)
  } finally {
    confirmingRevert.value = ''
    await revertsQuery.refetch()
  }
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Server"
      title="Services"
      description="Start, stop, restart, and toggle boot-time autorun for the system services on this node. Actions are applied through reviewed, agent-signed plans."
    >
      <StatusPill tone="accent" label="systemd" :pulse="false" />
    </PageHeader>

    <LockoutRevertPanel
      :reverts="reverts"
      :confirming="confirmingRevert"
      :disabled="!canWrite"
      @confirm="confirmRevert"
    />

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
    <JobProgress
      v-if="runner.progress.value"
      :event="runner.progress.value"
      v-bind="runner.progressProps.value"
    />

    <AppAlert v-if="servicesQuery.isError.value" tone="danger">
      Services couldn't be loaded. The node agent may be unreachable.
    </AppAlert>
    <EmptyState
      v-else-if="!servicesQuery.isPending.value && !services.length"
      icon="server"
      title="No managed services found"
      description="Once services like nginx, cron, a database engine, or PHP-FPM are installed on the node, they appear here."
    />

    <template v-else>
      <ListToolbar
        :search="collection.search.value"
        :count="collection.matching.value"
        count-label="services"
        placeholder="Search services"
        @update:search="collection.search.value = $event"
      />

      <div class="overflow-hidden rounded-xl border border-outline">
        <table class="w-full text-sm">
          <thead class="bg-raised/50 text-left text-[11px] font-semibold tracking-[0.1em] text-ink-muted uppercase">
            <tr>
              <th class="px-4 py-3">Name</th>
              <th class="px-4 py-3">Status</th>
              <th class="px-4 py-3">Autorun</th>
              <th class="w-40 px-4 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-outline">
            <template v-if="servicesQuery.isPending.value">
              <tr v-for="n in 8" :key="n">
                <td colspan="4" class="px-4 py-2"><SkeletonRow /></td>
              </tr>
            </template>
            <tr v-else-if="!collection.matching.value">
              <td colspan="4" class="px-4 py-10 text-center text-ink-muted">No services match your search.</td>
            </tr>
            <tr v-for="service in collection.items.value" v-else :key="service.systemdUnit" class="hover:bg-raised/30">
              <td class="px-4 py-3 font-mono text-ink">{{ service.name }}</td>
              <td class="px-4 py-3">
                <StatusPill :status="service.status" :pulse="false" />
              </td>
              <td class="px-4 py-3">
                <Switch
                  :model-value="service.enabled"
                  :disabled="!canWrite || runner.busy.value"
                  :aria-label="`Toggle autorun for ${service.name}`"
                  @update:model-value="toggleAutorun(service, $event)"
                />
              </td>
              <td class="px-4 py-3 text-right whitespace-nowrap">
                <AppButton
                  v-if="service.active"
                  size="sm"
                  variant="ghost"
                  icon="stop"
                  title="Stop"
                  :disabled="!canWrite || runner.busy.value"
                  :loading="pendingUnit === service.systemdUnit"
                  @click="requestRun(service, 'stop', 'stopped')"
                />
                <AppButton
                  v-else
                  size="sm"
                  variant="ghost"
                  icon="play"
                  title="Start"
                  :disabled="!canWrite || runner.busy.value"
                  :loading="pendingUnit === service.systemdUnit"
                  @click="requestRun(service, 'start', 'started')"
                />
                <AppButton
                  size="sm"
                  variant="ghost"
                  icon="refresh-cw"
                  title="Restart"
                  :disabled="!canWrite || runner.busy.value || !service.active"
                  :loading="pendingUnit === service.systemdUnit && service.active"
                  @click="requestRun(service, 'restart', 'restarted')"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager
        v-if="collection.matching.value"
        v-model:page="collection.page.value"
        v-model:page-size="collection.pageSize.value"
        :page-count="collection.pageCount.value"
        :total="collection.matching.value"
        :range-start="collection.rangeStart.value"
        :range-end="collection.rangeEnd.value"
        label="services"
      />
    </template>

    <AppConfirmDialog
      :open="Boolean(criticalStop)"
      :title="`Stop ${criticalStop?.name ?? 'service'}?`"
      :confirm-label="`Stop ${criticalStop?.name ?? 'service'}`"
      :type-to-confirm="criticalStop?.name"
      @confirm="confirmCriticalStop"
      @close="criticalStop = undefined"
    >
      Stopping this service can disconnect the panel or your server session. Keep an independent terminal open before
      continuing. Type the service name to confirm the interruption.
    </AppConfirmDialog>

    <AppConfirmDialog
      :open="Boolean(lockoutRisk)"
      :title="`Stopping ${lockoutRisk?.service.name ?? 'this service'} will cut off access`"
      confirm-label="Stop it anyway, with a rollback"
      :type-to-confirm="lockoutRisk?.service.name"
      @confirm="acceptLockoutRisk"
      @close="lockoutRisk = undefined"
    >
      {{ lockoutRisk?.message }} If you continue, the service is stopped and then started again automatically unless
      you confirm from a working session within the rollback window shown at the top of this page.
    </AppConfirmDialog>
  </section>
</template>
