<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
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

import { listServices, serviceAction, type Service, type ServiceAction } from '../api'

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

const runner = useJobRunner()
const pendingUnit = ref('')

async function run(service: Service, action: ServiceAction, successVerb: string) {
  if (!canWrite.value || runner.busy.value) return
  pendingUnit.value = service.systemdUnit
  await runner.run(async () => (await serviceAction(service.systemdUnit, action)).job.id, {
    onSettled: async () => {
      await servicesQuery.refetch()
    },
    successToast: `${service.name} ${successVerb}`,
    failureMessage: `Could not ${action} ${service.name}`,
  })
  pendingUnit.value = ''
}

function toggleAutorun(service: Service, enabled: boolean) {
  run(service, enabled ? 'enable' : 'disable', enabled ? 'enabled on boot' : 'disabled on boot')
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

    <JobFailureNotice v-if="runner.error.value" :message="runner.error.value" :job-id="runner.jobId.value" />
    <JobProgress
      v-if="runner.progress.value"
      :event="runner.progress.value"
      :messages="runner.messages.value"
      :started-at-ms="runner.startedAtMs.value"
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
                  @click="run(service, 'stop', 'stopped')"
                />
                <AppButton
                  v-else
                  size="sm"
                  variant="ghost"
                  icon="play"
                  title="Start"
                  :disabled="!canWrite || runner.busy.value"
                  :loading="pendingUnit === service.systemdUnit"
                  @click="run(service, 'start', 'started')"
                />
                <AppButton
                  size="sm"
                  variant="ghost"
                  icon="refresh-cw"
                  title="Restart"
                  :disabled="!canWrite || runner.busy.value || !service.active"
                  :loading="pendingUnit === service.systemdUnit && service.active"
                  @click="run(service, 'restart', 'restarted')"
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
  </section>
</template>
