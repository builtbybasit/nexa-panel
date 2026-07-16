<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatBytes, formatDateTime } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, AppIcon, AppInput, AppSelect, EmptyState, PageHeader, StatusPill } from '@/shared/ui'

import { listSites } from '../../sites/api'
import { downloadUrl, listLogs, readLog, subscribeToLogStream, type LogFile } from '../api'

// --- Site selection (persisted in the ?site= route query) ---
// Logs are useful for debugging failed/rolled-back sites too, so every status
// is eligible except the pre-provisioning ones where logs/ may not exist yet.

const route = useRoute()
const router = useRouter()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const preProvisionStatuses = new Set(['draft', 'planning', 'plan_ready'])
const eligibleSites = computed(() => (sitesQuery.data.value ?? []).filter((site) => !preProvisionStatuses.has(site.status)))

const siteId = computed(() => (typeof route.query.site === 'string' ? route.query.site : ''))
const selectedSite = computed(() => eligibleSites.value.find((site) => site.id === siteId.value))

function selectSite(id: string) {
  const query = { ...route.query }
  if (id) query.site = id
  else delete query.site
  void router.replace({ query })
}

const siteSelection = computed<string>({
  get: () => siteId.value,
  set: (value) => selectSite(value),
})

watch(
  eligibleSites,
  (sites) => {
    const first = sites[0]
    if (!siteId.value && first) selectSite(first.id)
  },
  { immediate: true },
)

// --- Discovered log files (left panel) ---

const logsQuery = useQuery({
  queryKey: ['logs', siteId],
  queryFn: () => listLogs(siteId.value),
  enabled: computed(() => Boolean(selectedSite.value)),
  retry: false,
})
const logFiles = computed(() => logsQuery.data.value ?? [])
const logsError = computed(() => (logsQuery.error.value instanceof Error ? logsQuery.error.value.message : ''))

const selectedName = ref<string>()

watch(logFiles, (files) => {
  if (selectedName.value && files.some((file) => file.name === selectedName.value)) return
  selectedName.value = files[0]?.name
})

function fileSize(file: LogFile): string {
  return file.size === 0 ? '0 B' : formatBytes(file.size)
}

// --- Line buffer (shared by tail reads and the live stream) ---

interface ViewerLine {
  id: number
  text: string
  /** Rotation markers rendered distinctly between log lines. */
  marker?: boolean
}

const LINE_BUFFER_CAP = 5000

const lines = ref<ViewerLine[]>([])
let nextLineId = 0

function appendLines(texts: string[]) {
  for (const text of texts) lines.value.push({ id: nextLineId++, text })
  if (lines.value.length > LINE_BUFFER_CAP) lines.value.splice(0, lines.value.length - LINE_BUFFER_CAP)
  void scrollToBottom()
}

function appendMarker(text: string) {
  lines.value.push({ id: nextLineId++, text, marker: true })
  void scrollToBottom()
}

// --- Auto-scroll with scroll-lock detection ---
// Appends keep the view pinned to the newest line unless the user scrolled up;
// scrolling back to (near) the bottom re-engages following the tail.

const logContainer = ref<HTMLElement>()
const stickToBottom = ref(true)

function onScroll() {
  const element = logContainer.value
  if (!element) return
  stickToBottom.value = element.scrollTop + element.clientHeight >= element.scrollHeight - 24
}

async function scrollToBottom(force = false) {
  if (!force && !stickToBottom.value) return
  await nextTick()
  const element = logContainer.value
  if (element) element.scrollTop = element.scrollHeight
}

function jumpToLatest() {
  stickToBottom.value = true
  void scrollToBottom(true)
}

// --- Viewer controls ---

const tailCount = ref('500')
const wrapLines = ref(false)
const viewerLoading = ref(false)
const viewerError = ref('')
const streamNotice = ref('')
const truncated = ref(false)

const filterInput = ref('')
const appliedFilter = ref('')
let filterTimer: number | undefined

watch(filterInput, (value) => {
  window.clearTimeout(filterTimer)
  filterTimer = window.setTimeout(() => {
    appliedFilter.value = value.trim()
  }, 400)
})

async function refreshTail() {
  const name = selectedName.value
  if (!name || !selectedSite.value) return
  viewerLoading.value = true
  viewerError.value = ''
  try {
    const filter = appliedFilter.value
    const result = await readLog(siteId.value, { name, tail: Number(tailCount.value), ...(filter ? { filter } : {}) })
    lines.value = result.lines.map((text) => ({ id: nextLineId++, text }))
    truncated.value = result.truncated
    stickToBottom.value = true
    void scrollToBottom(true)
  } catch (caught) {
    viewerError.value = caught instanceof Error ? caught.message : 'The log read failed.'
  } finally {
    viewerLoading.value = false
  }
}

// --- Follow (live tail over SSE) ---
// The stream opens with its own initial tail, so the buffer is cleared on
// start to avoid duplicating the last manual read. Stopped on toggle-off,
// site change, file change, and unmount.

const following = ref(false)
let stopStream: (() => void) | undefined

function startFollow() {
  stopFollow()
  const name = selectedName.value
  if (!name || !selectedSite.value) return
  lines.value = []
  truncated.value = false
  viewerError.value = ''
  streamNotice.value = ''
  stickToBottom.value = true
  following.value = true
  stopStream = subscribeToLogStream(siteId.value, name, appliedFilter.value, {
    onLines: (event) => {
      if (event.rotated) appendMarker('— log rotated; continuing from the new file —')
      if (event.lines.length) appendLines(event.lines)
    },
    onError: () => {
      stopStream = undefined
      following.value = false
      streamNotice.value = 'The live stream ended. Toggle Follow to reconnect, or Refresh for a snapshot.'
    },
  })
}

function stopFollow() {
  stopStream?.()
  stopStream = undefined
  following.value = false
}

function toggleFollow() {
  if (following.value) stopFollow()
  else startFollow()
}

// --- Lifecycle: reset the viewer on site/file change, clean up on unmount ---

watch(siteId, () => {
  stopFollow()
  selectedName.value = undefined
  lines.value = []
  viewerError.value = ''
  streamNotice.value = ''
  truncated.value = false
})

watch(selectedName, (name) => {
  stopFollow()
  lines.value = []
  viewerError.value = ''
  streamNotice.value = ''
  truncated.value = false
  if (name) void refreshTail()
})

watch(tailCount, () => {
  if (!following.value) void refreshTail()
})

watch(appliedFilter, () => {
  if (following.value) startFollow()
  else void refreshTail()
})

onBeforeUnmount(() => {
  stopFollow()
  window.clearTimeout(filterTimer)
})
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Logs"
      description="Inspect and live-tail the nginx, PHP, and task logs under each site's logs/ directory."
    />

    <AppAlert v-if="sitesQuery.isPending.value" tone="info">Loading sites…</AppAlert>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">The site list is unavailable.</AppAlert>
    <EmptyState
      v-else-if="!eligibleSites.length"
      icon="file-text"
      title="No provisioned sites"
      description="Logs become available once a site has been provisioned on the host."
    />

    <template v-else>
      <div class="flex flex-wrap items-center gap-3">
        <div class="w-full sm:w-72">
          <AppSelect v-model="siteSelection" aria-label="Site">
            <option v-for="site in eligibleSites" :key="site.id" :value="site.id">
              {{ site.displayName }} — {{ site.primaryDomain }}
            </option>
          </AppSelect>
        </div>
        <StatusPill v-if="selectedSite" :status="selectedSite.status" />
      </div>

      <AppAlert v-if="siteId && sitesQuery.isSuccess.value && !selectedSite" tone="warning">
        The selected site has no logs yet or is not accessible. Choose another site.
      </AppAlert>

      <div v-if="selectedSite" class="grid gap-4 lg:grid-cols-[300px_minmax(0,1fr)] lg:items-start">
        <!-- Discovered log files -->
        <AppCard eyebrow="Discovered" title="Log files" flush>
          <template #actions>
            <AppButton
              size="sm"
              variant="ghost"
              icon="refresh-cw"
              :loading="logsQuery.isFetching.value"
              aria-label="Refresh log files"
              @click="logsQuery.refetch()"
            />
          </template>
          <div class="px-2 pb-2">
            <AppAlert v-if="logsQuery.isPending.value" tone="info" class="m-2">Loading log files…</AppAlert>
            <AppAlert v-else-if="logsError" tone="danger" class="m-2">{{ logsError }}</AppAlert>
            <EmptyState
              v-else-if="!logFiles.length"
              icon="file-text"
              title="No log files"
              description="Nothing has been written under logs/ for this site yet."
              class="m-2"
            />
            <ul v-else class="max-h-[560px] space-y-0.5 overflow-y-auto">
              <li v-for="file in logFiles" :key="file.name">
                <button
                  class="w-full rounded-lg px-2.5 py-2 text-left transition-colors"
                  :class="
                    file.name === selectedName
                      ? 'bg-accent-400/[0.08] text-ink shadow-[inset_2px_0_0] shadow-accent-400'
                      : 'text-ink-secondary hover:bg-white/[0.05] hover:text-ink'
                  "
                  :aria-current="file.name === selectedName ? 'true' : undefined"
                  @click="selectedName = file.name"
                >
                  <span class="flex items-center gap-2">
                    <AppIcon name="file-text" :size="14" class="shrink-0 text-ink-muted" />
                    <span class="min-w-0 truncate font-mono text-[13px] font-medium">{{ file.name }}</span>
                  </span>
                  <span class="mt-0.5 block pl-6 text-[11px] text-ink-muted">
                    {{ fileSize(file) }} · {{ formatDateTime(file.modifiedAt) }}
                  </span>
                </button>
              </li>
            </ul>
          </div>
        </AppCard>

        <!-- Log viewer -->
        <AppCard eyebrow="Viewer" :title="selectedName ?? 'Log viewer'" flush>
          <template #actions>
            <StatusPill v-if="following" tone="success" label="Live" pulse />
            <StatusPill v-if="truncated" tone="warning" label="Truncated read" :pulse="false" />
          </template>
          <div class="space-y-3 px-5 pb-5 sm:px-6 sm:pb-6">
            <div class="flex flex-wrap items-center gap-2">
              <div class="w-32">
                <AppSelect v-model="tailCount" aria-label="Line count" :disabled="following">
                  <option value="200">200 lines</option>
                  <option value="500">500 lines</option>
                  <option value="2000">2000 lines</option>
                </AppSelect>
              </div>
              <div class="min-w-0 flex-1 sm:max-w-xs">
                <AppInput v-model="filterInput" placeholder="Filter lines (server-side)" aria-label="Filter" autocomplete="off" />
              </div>
              <AppButton size="sm" icon="refresh-cw" :loading="viewerLoading" :disabled="following || !selectedName" @click="refreshTail">
                Refresh
              </AppButton>
              <AppButton
                size="sm"
                :variant="following ? 'primary' : 'secondary'"
                :icon="following ? 'stop' : 'play'"
                :disabled="!selectedName"
                @click="toggleFollow"
              >
                {{ following ? 'Following' : 'Follow' }}
              </AppButton>
              <a
                v-if="selectedName"
                :href="downloadUrl(siteId, selectedName)"
                class="inline-flex h-8 items-center justify-center gap-1.5 rounded-lg border border-outline-strong bg-white/[0.03] px-3 text-[13px] font-medium text-ink transition-colors duration-150 hover:bg-white/[0.07]"
              >
                <AppIcon name="download" :size="14" />
                Download
              </a>
              <label class="flex cursor-pointer items-center gap-2 text-[13px] text-ink-secondary">
                <input v-model="wrapLines" type="checkbox" class="accent-accent-500" />
                Wrap lines
              </label>
            </div>

            <AppAlert v-if="viewerError" tone="danger">{{ viewerError }}</AppAlert>
            <AppAlert v-if="streamNotice" tone="warning">{{ streamNotice }}</AppAlert>

            <EmptyState
              v-if="!selectedName"
              icon="file-text"
              title="No log selected"
              description="Pick a log file from the list to view its tail."
            />
            <div v-else class="relative">
              <div
                ref="logContainer"
                class="h-[520px] overflow-auto rounded-xl border border-outline bg-canvas/60 p-3 font-mono text-xs leading-5 text-ink-secondary"
                tabindex="0"
                role="log"
                :aria-label="`Log lines for ${selectedName}`"
                @scroll="onScroll"
              >
                <p v-if="viewerLoading && !lines.length" class="text-ink-muted">Loading log…</p>
                <p v-else-if="!lines.length" class="text-ink-muted">
                  {{ appliedFilter ? 'No lines matched the filter.' : following ? 'Waiting for new lines…' : 'The log is empty.' }}
                </p>
                <template v-else>
                  <div
                    v-for="line in lines"
                    :key="line.id"
                    :class="[
                      wrapLines ? 'whitespace-pre-wrap break-all' : 'w-max min-w-full whitespace-pre',
                      line.marker ? 'my-1 rounded border-y border-amber-400/25 bg-amber-400/[0.07] px-1.5 py-0.5 text-amber-300' : '',
                    ]"
                  >
                    {{ line.text || ' ' }}
                  </div>
                </template>
              </div>
              <button
                v-if="!stickToBottom && lines.length"
                class="absolute right-3 bottom-3 inline-flex h-8 items-center gap-1.5 rounded-lg border border-outline-strong bg-raised px-3 text-[13px] font-medium text-ink shadow-lg transition-colors hover:bg-white/[0.07]"
                @click="jumpToLatest"
              >
                Jump to latest
              </button>
            </div>
          </div>
        </AppCard>
      </div>
    </template>
  </section>
</template>
