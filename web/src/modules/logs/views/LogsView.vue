<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatBytes, formatDateTime } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, AppIcon, AppInput, EmptyState, PageHeader, SkeletonRow, StatusPill, Switch } from '@/shared/ui'
import { Combobox, ComboboxEmpty, ComboboxFloatingContent, ComboboxSelectItem, ComboboxTriggerInput } from '@/shared/ui/combobox'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'

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
  /** Rotation/trim markers rendered distinctly between log lines. */
  marker?: boolean
  /** True for the single pinned marker noting trimmed output. */
  trim?: boolean
}

const LINE_BUFFER_CAP = 5000
const TRIM_MARKER_TEXT = '— older lines were dropped (5,000-line buffer) — download the file for the full log —'

const lines = ref<ViewerLine[]>([])
let nextLineId = 0

function appendLines(texts: string[]) {
  for (const text of texts) lines.value.push({ id: nextLineId++, text })
  const overflow = lines.value.length - LINE_BUFFER_CAP
  if (overflow > 0) {
    lines.value.splice(0, overflow)
    // Keep a single marker pinned at the top so the discarded output is visible.
    if (!lines.value[0]?.trim) lines.value.unshift({ id: nextLineId++, text: TRIM_MARKER_TEXT, marker: true, trim: true })
  }
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

// While following, the stream is unfiltered and the filter is applied here
// instead, so changing it never wipes the accumulated buffer. Snapshot reads
// still filter server-side, which makes this a no-op for them.
const visibleLines = computed(() => {
  const needle = appliedFilter.value.toLowerCase()
  if (!needle) return lines.value
  return lines.value.filter((line) => line.marker || line.text.toLowerCase().includes(needle))
})

// --- In-buffer search with match highlighting ---

const searchInput = ref('')
const searchTerm = ref('')
let searchTimer: number | undefined

watch(searchInput, (value) => {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    searchTerm.value = value.trim()
  }, 300)
})

const matchCount = computed(() => {
  const needle = searchTerm.value.toLowerCase()
  if (!needle) return 0
  let count = 0
  for (const line of visibleLines.value) {
    if (line.marker) continue
    const haystack = line.text.toLowerCase()
    let index = haystack.indexOf(needle)
    while (index !== -1) {
      count++
      index = haystack.indexOf(needle, index + needle.length)
    }
  }
  return count
})

interface LineSegment {
  text: string
  hit: boolean
}

function lineSegments(text: string): LineSegment[] {
  const needle = searchTerm.value
  if (!needle) return [{ text, hit: false }]
  const haystack = text.toLowerCase()
  const needleLower = needle.toLowerCase()
  const segments: LineSegment[] = []
  let cursor = 0
  let index = haystack.indexOf(needleLower)
  while (index !== -1) {
    if (index > cursor) segments.push({ text: text.slice(cursor, index), hit: false })
    segments.push({ text: text.slice(index, index + needle.length), hit: true })
    cursor = index + needle.length
    index = haystack.indexOf(needleLower, cursor)
  }
  if (cursor < text.length) segments.push({ text: text.slice(cursor), hit: false })
  return segments
}

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
// site change, file change, and unmount. The stream itself is unfiltered:
// `visibleLines` filters client-side so filter changes keep the buffer.

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
  stopStream = subscribeToLogStream(siteId.value, name, '', {
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

// While following, `visibleLines` applies the new filter over the kept buffer;
// only snapshot mode needs a re-read.
watch(appliedFilter, () => {
  if (!following.value) void refreshTail()
})

onBeforeUnmount(() => {
  stopFollow()
  window.clearTimeout(filterTimer)
  window.clearTimeout(searchTimer)
})
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Web hosting"
      title="Logs"
      description="Inspect and live-tail the nginx, PHP, and task logs under each site's logs/ directory."
    />

    <AppCard v-if="sitesQuery.isPending.value" flush>
      <div class="divide-y divide-outline px-4 py-1 sm:px-5">
        <SkeletonRow v-for="n in 3" :key="n" />
      </div>
    </AppCard>
    <AppAlert v-else-if="sitesQuery.isError.value" tone="danger">
      <p>The site list couldn't be loaded.</p>
      <AppButton size="sm" class="mt-2" @click="sitesQuery.refetch()">Retry</AppButton>
    </AppAlert>
    <EmptyState
      v-else-if="!eligibleSites.length"
      icon="file-text"
      title="No provisioned sites"
      description="Logs become available once a site has been provisioned on the host."
    />

    <template v-else>
      <div class="flex flex-wrap items-center gap-3">
        <div class="w-full sm:w-72">
          <Combobox v-model="siteSelection">
            <ComboboxTriggerInput
              aria-label="Site"
              placeholder="Select a site"
              :display-value="(id) => {
                const site = eligibleSites.find((s) => s.id === id)
                return site ? `${site.displayName} — ${site.primaryDomain}` : ''
              }"
            />
            <ComboboxFloatingContent>
              <ComboboxEmpty>No sites match.</ComboboxEmpty>
              <ComboboxSelectItem
                v-for="site in eligibleSites"
                :key="site.id"
                :value="site.id"
                :text-value="`${site.displayName} ${site.primaryDomain}`"
              >
                {{ site.displayName }} — {{ site.primaryDomain }}
              </ComboboxSelectItem>
            </ComboboxFloatingContent>
          </Combobox>
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
            <div v-if="logsQuery.isPending.value" class="divide-y divide-outline px-2">
              <SkeletonRow v-for="n in 3" :key="n" />
            </div>
            <AppAlert v-else-if="logsError" tone="danger" class="m-2">
              <p>Log files couldn't be loaded: {{ logsError }}</p>
              <AppButton size="sm" class="mt-2" @click="logsQuery.refetch()">Retry</AppButton>
            </AppAlert>
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
                <Select v-model="tailCount" :disabled="following">
                  <SelectTrigger aria-label="Line count" />
                  <SelectContent>
                    <SelectItem value="200">200 lines</SelectItem>
                    <SelectItem value="500">500 lines</SelectItem>
                    <SelectItem value="2000">2000 lines</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div class="min-w-0 flex-1 sm:max-w-xs">
                <AppInput v-model="filterInput" placeholder="Filter lines" aria-label="Filter lines" autocomplete="off" />
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
              <label class="flex items-center gap-2 text-[13px] text-ink-secondary">
                <Switch v-model="wrapLines" />
                Wrap lines
              </label>
            </div>

            <div class="flex flex-wrap items-center gap-2">
              <div class="relative min-w-0 flex-1 sm:max-w-xs">
                <AppIcon
                  name="search"
                  :size="15"
                  class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-ink-muted"
                />
                <AppInput
                  v-model="searchInput"
                  class="pl-9"
                  placeholder="Search loaded lines"
                  aria-label="Search loaded lines"
                  autocomplete="off"
                />
              </div>
              <span v-if="searchTerm" class="text-[13px] whitespace-nowrap text-ink-muted tabular-nums" aria-live="polite">
                {{ matchCount }} {{ matchCount === 1 ? 'match' : 'matches' }}
              </span>
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
                <p v-else-if="!visibleLines.length" class="text-ink-muted">
                  {{
                    lines.length || appliedFilter
                      ? 'No lines match the filter.'
                      : following
                        ? 'Waiting for new lines…'
                        : 'The log is empty.'
                  }}
                </p>
                <template v-else>
                  <div
                    v-for="line in visibleLines"
                    :key="line.id"
                    :class="[
                      wrapLines ? 'whitespace-pre-wrap break-all' : 'w-max min-w-full whitespace-pre',
                      line.marker ? 'my-1 rounded border-y border-amber-400/25 bg-amber-400/[0.07] px-1.5 py-0.5 text-amber-300' : '',
                    ]"
                  >
                    <template v-if="!line.marker && searchTerm"
                      ><span
                        v-for="(segment, index) in lineSegments(line.text)"
                        :key="index"
                        :class="segment.hit ? 'rounded-[2px] bg-amber-400/30 text-amber-100' : ''"
                        >{{ segment.text }}</span
                      ></template
                    >
                    <template v-else>{{ line.text || ' ' }}</template>
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
