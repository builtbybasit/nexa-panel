<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, onBeforeUnmount, ref } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, MetricCard, PageHeader, ProgressBar, SkeletonCard, StatusPill } from '@/shared/ui'

import { getSystemOverview } from '../api'

const { data, isPending, isError, isFetching, refetch } = useQuery({
  queryKey: ['system', 'overview'],
  queryFn: getSystemOverview,
  refetchInterval: 15_000,
})

const memory = computed(() => data.value?.memory)
const memoryPercent = computed(() => Math.round(memory.value?.usedPercent ?? 0))
const memoryTone = computed(() => (memoryPercent.value >= 90 ? 'danger' : memoryPercent.value >= 75 ? 'warning' : 'accent'))

const podmanInstallCommand = 'sudo apt install podman'
const installCommandCopied = ref(false)
const installCommandCopyFailed = ref(false)
let copiedResetTimer: ReturnType<typeof setTimeout> | undefined

async function copyInstallCommand() {
  installCommandCopyFailed.value = false
  try {
    await navigator.clipboard.writeText(podmanInstallCommand)
    installCommandCopied.value = true
    clearTimeout(copiedResetTimer)
    copiedResetTimer = setTimeout(() => {
      installCommandCopied.value = false
    }, 2_000)
  } catch {
    installCommandCopied.value = false
    installCommandCopyFailed.value = true
  }
}

onBeforeUnmount(() => clearTimeout(copiedResetTimer))
</script>

<template>
  <section class="space-y-6">
    <PageHeader eyebrow="Observed state" title="Local node capacity">
      <AppButton icon="refresh-cw" :loading="isFetching" @click="refetch()">Refresh</AppButton>
    </PageHeader>

    <div v-if="isPending" class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      <SkeletonCard v-for="n in 3" :key="n" />
    </div>

    <div v-else-if="isError" class="flex flex-col items-start gap-3">
      <AppAlert tone="danger" class="w-full">
        The control plane is unavailable. Start <code class="font-mono">nexa api</code> and try again.
      </AppAlert>
      <AppButton icon="refresh-cw" :loading="isFetching" @click="refetch()">Retry</AppButton>
    </div>

    <template v-else-if="data">
      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <MetricCard
          label="Memory profile"
          icon="cpu"
          :value="memory?.profile ?? '—'"
          :detail="memory ? `${formatBytes(memory.totalBytes)} total` : 'Capacity not reported'"
          class="capitalize"
        />
        <MetricCard
          label="Memory available"
          icon="activity"
          :value="formatBytes(memory?.availableBytes)"
          :detail="memory ? `${memoryPercent}% currently used` : 'Capacity not reported'"
        >
          <template v-if="memory">
            <ProgressBar :value="memoryPercent" class="mt-3" :tone="memoryTone" />
            <p class="mt-2 text-xs text-ink-muted">Keep usage under 90% to protect provisioning headroom</p>
          </template>
        </MetricCard>
        <MetricCard
          label="Swap"
          icon="server"
          :value="formatBytes(memory?.swapTotalBytes)"
          :detail="
            memory
              ? memory.swapTotalBytes
                ? `${formatBytes(memory.swapFreeBytes)} free`
                : 'No swap configured'
              : 'Capacity not reported'
          "
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
            :description="
              data.podman.available
                ? 'Podman is installed and ready to run container tools'
                : 'Podman is not installed — container tools are unavailable until it is'
            "
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
        <div v-if="!data.podman.available" class="mt-4">
          <div class="flex flex-wrap items-center gap-2">
            <p class="text-[13px] text-ink-secondary">
              Install Podman with the node's package manager — on Debian or Ubuntu:
            </p>
            <code class="rounded-lg border border-outline bg-white/[0.03] px-2 py-1 font-mono text-xs text-accent-200">
              {{ podmanInstallCommand }}
            </code>
            <AppButton
              size="sm"
              variant="ghost"
              :icon="installCommandCopied ? 'check' : 'copy'"
              @click="copyInstallCommand"
            >
              {{ installCommandCopied ? 'Copied' : 'Copy command' }}
            </AppButton>
          </div>
          <span role="status" aria-live="polite" class="sr-only">
            {{ installCommandCopied ? 'Command copied to clipboard' : '' }}
          </span>
          <p v-if="installCommandCopyFailed" role="alert" class="mt-1.5 text-xs text-rose-300">
            Copy failed — select the command text and copy it manually
          </p>
        </div>
      </AppCard>

      <div v-if="data.warnings.length" class="space-y-2">
        <AppAlert v-for="warning in data.warnings" :key="warning" tone="warning">{{ warning }}</AppAlert>
      </div>
    </template>
  </section>
</template>
