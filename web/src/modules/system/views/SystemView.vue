<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, MetricCard, PageHeader, ProgressBar, StatusPill } from '@/shared/ui'

import { getSystemOverview } from '../api'

const { data, isPending, isError, isFetching, refetch } = useQuery({
  queryKey: ['system', 'overview'],
  queryFn: getSystemOverview,
  refetchInterval: 15_000,
})

const memory = computed(() => data.value?.memory)
const memoryPercent = computed(() => Math.round(memory.value?.usedPercent ?? 0))
</script>

<template>
  <section class="space-y-6">
    <PageHeader eyebrow="Observed state" title="Local node capacity">
      <AppButton icon="refresh-cw" :loading="isFetching" @click="refetch()">Refresh</AppButton>
    </PageHeader>

    <AppAlert v-if="isPending" tone="info">Reading the node's capabilities…</AppAlert>
    <AppAlert v-else-if="isError" tone="danger">
      The control plane is unavailable. Start <code class="font-mono">nexa api</code> and try again.
    </AppAlert>

    <template v-else-if="data">
      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <MetricCard
          label="Memory profile"
          icon="cpu"
          :value="memory?.profile ?? 'Development'"
          :detail="`${formatBytes(memory?.totalBytes)} total`"
          class="capitalize"
        />
        <MetricCard
          label="Memory available"
          icon="activity"
          :value="formatBytes(memory?.availableBytes)"
          :detail="`${memoryPercent}% currently used`"
        >
          <ProgressBar :value="memoryPercent" class="mt-3" :tone="memoryPercent > 90 ? 'danger' : 'accent'" />
        </MetricCard>
        <MetricCard
          label="Swap"
          icon="server"
          :value="formatBytes(memory?.swapTotalBytes)"
          :detail="memory?.swapTotalBytes ? `${formatBytes(memory?.swapFreeBytes)} free` : 'No swap configured'"
        />
      </div>

      <AppCard
        eyebrow="Container capability"
        title="Podman runtime"
        description="Podman runs isolated administration tools such as phpMyAdmin and pgAdmin. Core Nginx, PHP-FPM, and PostgreSQL processes remain native to protect the compact memory profile."
      >
        <template #actions>
          <StatusPill
            :tone="data.podman.available ? 'success' : 'warning'"
            :label="data.podman.available ? 'Available' : 'Action needed'"
          />
        </template>
        <dl class="grid gap-x-6 gap-y-3 text-[13px] sm:grid-cols-2">
          <div>
            <dt class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Version</dt>
            <dd class="mt-1 text-ink">{{ data.podman.version ?? 'Install Podman on the managed Linux node' }}</dd>
          </div>
          <div v-if="data.podman.path">
            <dt class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Binary</dt>
            <dd class="mt-1 font-mono text-xs text-accent-200">{{ data.podman.path }}</dd>
          </div>
        </dl>
      </AppCard>

      <div v-if="data.warnings.length" class="space-y-2">
        <AppAlert v-for="warning in data.warnings" :key="warning" tone="warning">{{ warning }}</AppAlert>
      </div>
    </template>
  </section>
</template>
