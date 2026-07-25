<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { listJobs, type Job } from '@/modules/jobs/api'
import { formatJobKind } from '@/shared/formatters'
import { AppIcon, Popover, PopoverContent, PopoverTrigger, ProgressBar, StatusPill } from '@/shared/ui'

const identity = useIdentityStore()

// Shares the ['jobs'] cache entry with JobsView/Overview so the endpoint is
// polled once; the shortest active observer interval wins.
const jobsQuery = useQuery({
  queryKey: ['jobs'],
  queryFn: listJobs,
  refetchInterval: 5_000,
  retry: false,
  enabled: computed(() => identity.authenticated),
})

const activeJobs = computed(() => (jobsQuery.data.value ?? []).filter((job) => job.state === 'queued' || job.state === 'running'))

/** Jobs seen active earlier in this page load that have since finished. */
const settledJobs = ref<Job[]>([])
const seenActiveIds = new Set<number>()

watch(jobsQuery.data, (jobs) => {
  for (const job of jobs ?? []) {
    if (job.state === 'queued' || job.state === 'running') {
      seenActiveIds.add(job.id)
    } else if (seenActiveIds.has(job.id)) {
      seenActiveIds.delete(job.id)
      settledJobs.value = [job, ...settledJobs.value.filter((settled) => settled.id !== job.id)].slice(0, 5)
    }
  }
})

const open = ref(false)

const visible = computed(() => activeJobs.value.length > 0 || settledJobs.value.length > 0)

// Closing when the last entry disappears would strand a stale popover.
watch(visible, (nowVisible) => {
  if (!nowVisible) open.value = false
})
</script>

<template>
  <Popover v-if="visible" v-model:open="open">
    <PopoverTrigger as-child>
      <button
        type="button"
        class="relative grid size-9 place-items-center rounded-lg border border-outline text-ink-secondary transition-colors hover:border-outline-strong hover:text-ink"
        :aria-label="activeJobs.length ? `${activeJobs.length} active ${activeJobs.length === 1 ? 'job' : 'jobs'}` : 'Recently finished jobs'"
      >
        <AppIcon v-if="activeJobs.length" name="loader" :size="16" class="animate-spin text-accent-300" />
        <AppIcon v-else name="check" :size="16" />
        <span
          v-if="activeJobs.length"
          class="absolute -top-1.5 -right-1.5 grid min-w-4 place-items-center rounded-full bg-accent-500 px-1 text-[10px] leading-4 font-bold text-accent-950"
        >
          {{ activeJobs.length }}
        </span>
      </button>
    </PopoverTrigger>

    <PopoverContent align="end" class="w-72" aria-live="polite">
      <template v-if="activeJobs.length">
        <p class="px-2 pt-1 pb-1.5 text-[10px] font-bold tracking-[0.14em] text-ink-muted uppercase">Running now</p>
        <RouterLink
          v-for="job in activeJobs"
          :key="job.id"
          :to="`/jobs?job=${job.id}`"
          class="block rounded-lg px-2 py-2 transition-colors hover:bg-white/[0.04]"
          @click="open = false"
        >
          <span class="mb-1.5 flex items-center justify-between gap-2">
            <span class="truncate text-[13px] font-medium text-ink">{{ job.title || formatJobKind(job.kind) }}</span>
            <span class="shrink-0 text-[11px] text-ink-muted tabular-nums">#{{ job.id }}</span>
          </span>
          <ProgressBar :value="job.progress" />
        </RouterLink>
      </template>

      <template v-if="settledJobs.length">
        <p class="px-2 pt-2 pb-1.5 text-[10px] font-bold tracking-[0.14em] text-ink-muted uppercase">Finished recently</p>
        <RouterLink
          v-for="job in settledJobs"
          :key="job.id"
          :to="`/jobs?job=${job.id}`"
          class="flex items-center justify-between gap-2 rounded-lg px-2 py-2 transition-colors hover:bg-white/[0.04]"
          @click="open = false"
        >
          <span class="truncate text-[13px] text-ink-secondary">{{ job.title || formatJobKind(job.kind) }}</span>
          <StatusPill :status="job.state" />
        </RouterLink>
      </template>
    </PopoverContent>
  </Popover>

  <!-- Idle placeholder keeps the top bar from shifting when jobs appear. -->
  <span v-else class="inline-block size-9" aria-hidden="true" />
</template>
