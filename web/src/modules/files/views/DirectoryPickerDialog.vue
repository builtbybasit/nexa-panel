<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { AppButton, AppDialog, AppIcon } from '@/shared/ui'

import { listFiles } from '../api'

/** One directory in the lazily loaded tree; `children` is undefined until first expanded. */
interface DirNode {
  name: string
  path: string
  expanded: boolean
  loading: boolean
  error: string
  children?: DirNode[] | undefined
}

interface Row {
  key: string
  depth: number
  node?: DirNode
  note?: { text: string; tone: 'muted' | 'danger' }
}

const props = defineProps<{ open: boolean; siteId: string }>()
const emit = defineEmits<{ close: []; select: [path: string] }>()

const root = ref<DirNode>()
const currentPath = ref('.')

function makeNode(name: string, path: string): DirNode {
  return { name, path, expanded: false, loading: false, error: '' }
}

async function loadChildren(node: DirNode) {
  node.loading = true
  node.error = ''
  try {
    const listing = await listFiles(props.siteId, node.path)
    node.children = listing.items
      .filter((entry) => entry.kind === 'dir')
      .map((entry) => makeNode(entry.name, node.path === '.' ? entry.name : `${node.path}/${entry.name}`))
  } catch (caught) {
    node.error = caught instanceof Error ? caught.message : 'The folder could not be listed.'
    node.children = undefined
  } finally {
    node.loading = false
  }
}

// Rebuild the tree from the root every time the picker opens so it reflects
// the current state of the site.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    const rootNode = makeNode('/', '.')
    rootNode.expanded = true
    root.value = rootNode
    currentPath.value = '.'
    void loadChildren(rootNode)
  },
  { immediate: true },
)

/** Clicking a folder both selects it and toggles its children (loaded lazily). */
function activate(node: DirNode) {
  currentPath.value = node.path
  if (node.path !== '.') node.expanded = !node.expanded
  if (node.expanded && node.children === undefined && !node.loading) void loadChildren(node)
}

const rows = computed<Row[]>(() => {
  const out: Row[] = []
  const visit = (node: DirNode, depth: number) => {
    out.push({ key: `dir:${node.path}`, depth, node })
    if (!node.expanded) return
    if (node.loading) out.push({ key: `note:${node.path}:loading`, depth: depth + 1, note: { text: 'Loading…', tone: 'muted' } })
    else if (node.error) out.push({ key: `note:${node.path}:error`, depth: depth + 1, note: { text: node.error, tone: 'danger' } })
    else if (node.children && node.children.length === 0)
      out.push({ key: `note:${node.path}:empty`, depth: depth + 1, note: { text: 'No subfolders', tone: 'muted' } })
    else for (const child of node.children ?? []) visit(child, depth + 1)
  }
  if (root.value) visit(root.value, 0)
  return out
})

const crumbs = computed(() => {
  if (currentPath.value === '.') return [] as { name: string; path: string }[]
  const segments = currentPath.value.split('/')
  return segments.map((name, index) => ({ name, path: segments.slice(0, index + 1).join('/') }))
})

const displayCurrent = computed(() => (currentPath.value === '.' ? '/' : `/${currentPath.value}`))

// Destinations must sit inside a writable zone — the same gate the listing
// applies — so the picker never hands back a folder the server would reject.
const writeZones = new Set(['public', 'private', 'tmp', 'backups'])
const canUseCurrent = computed(
  () => currentPath.value !== '.' && writeZones.has(currentPath.value.split('/')[0] ?? ''),
)
</script>

<template>
  <AppDialog :open="open" title="Choose a folder" @close="emit('close')">
    <div class="space-y-3">
      <nav class="flex flex-wrap items-center gap-0.5 font-mono text-[13px]" aria-label="Chosen folder">
        <button
          type="button"
          class="rounded px-1.5 py-0.5 transition-colors hover:bg-white/[0.05] hover:text-ink"
          :class="currentPath === '.' ? 'text-ink' : 'text-ink-secondary'"
          @click="currentPath = '.'"
        >
          /
        </button>
        <template v-for="(crumb, index) in crumbs" :key="crumb.path">
          <AppIcon v-if="index > 0" name="chevron-right" :size="12" class="shrink-0 text-ink-muted" />
          <button
            type="button"
            class="truncate rounded px-1.5 py-0.5 transition-colors hover:bg-white/[0.05] hover:text-ink"
            :class="index === crumbs.length - 1 ? 'text-ink' : 'text-ink-secondary'"
            @click="currentPath = crumb.path"
          >
            {{ crumb.name }}
          </button>
        </template>
      </nav>

      <div class="max-h-72 overflow-y-auto rounded-xl border border-outline bg-canvas/40 p-1">
        <template v-for="row in rows" :key="row.key">
          <button
            v-if="row.node"
            type="button"
            class="flex w-full items-center gap-2 rounded-lg py-1.5 pr-2 text-left text-[13px] transition-colors"
            :class="row.node.path === currentPath ? 'bg-accent-500/10 text-ink' : 'text-ink-secondary hover:bg-white/[0.05] hover:text-ink'"
            :style="{ paddingLeft: `${row.depth * 16 + 8}px` }"
            :aria-expanded="row.node.expanded"
            :aria-current="row.node.path === currentPath ? 'true' : undefined"
            @click="activate(row.node)"
          >
            <AppIcon :name="row.node.expanded ? 'chevron-down' : 'chevron-right'" :size="12" class="shrink-0 text-ink-muted" />
            <AppIcon :name="row.node.expanded ? 'folder-open' : 'folder'" :size="14" class="shrink-0 text-accent-300" />
            <span class="truncate font-mono">{{ row.node.name }}</span>
          </button>
          <p
            v-else-if="row.note"
            class="py-1 pr-2 text-xs"
            :class="row.note.tone === 'danger' ? 'text-rose-300' : 'text-ink-muted'"
            :style="{ paddingLeft: `${row.depth * 16 + 8}px` }"
          >
            {{ row.note.text }}
          </p>
        </template>
      </div>
    </div>
    <template #footer>
      <span class="mr-auto min-w-0 truncate text-xs" aria-live="polite">
        <span class="font-mono text-ink-muted">{{ displayCurrent }}</span>
        <span v-if="!canUseCurrent" class="block text-amber-300">
          Pick a folder inside public/, private/, tmp/, or backups/.
        </span>
      </span>
      <AppButton @click="emit('close')">Cancel</AppButton>
      <AppButton variant="primary" :disabled="!canUseCurrent" @click="emit('select', currentPath)">
        Use this folder
      </AppButton>
    </template>
  </AppDialog>
</template>
