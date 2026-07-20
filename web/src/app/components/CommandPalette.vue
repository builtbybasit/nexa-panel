<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import {
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
  VisuallyHidden,
} from 'reka-ui'
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

import { listCertificates } from '@/modules/certificates/api'
import { listDatabases as listPostgresDatabases } from '@/modules/databases/api'
import { listDomains } from '@/modules/domains/api'
import { useIdentityStore } from '@/modules/identity/store'
import { listDatabases as listMysqlDatabases } from '@/modules/mysql/api'
import { featureModules } from '@/modules/registry'
import { listSites } from '@/modules/sites/api'
import { AppIcon, EmptyState } from '@/shared/ui'
import {
  Combobox,
  ComboboxContent,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxLabel,
} from '@/shared/ui/combobox'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()

const router = useRouter()
const identity = useIdentityStore()

const search = ref('')
/** Flips once on first open so resource lists are only fetched when needed. */
const hasOpened = ref(false)

watch(
  () => props.open,
  (open) => {
    if (open) {
      hasOpened.value = true
      search.value = ''
    }
  },
)

// Failures are silently skipped: an unreachable source simply contributes no results.
const lazy = { enabled: hasOpened, retry: false, staleTime: 60_000 } as const
const sitesQuery = useQuery({ queryKey: ['palette', 'sites'], queryFn: listSites, ...lazy })
const domainsQuery = useQuery({ queryKey: ['palette', 'domains'], queryFn: () => listDomains(), ...lazy })
const certificatesQuery = useQuery({ queryKey: ['palette', 'certificates'], queryFn: () => listCertificates(), ...lazy })
const postgresQuery = useQuery({ queryKey: ['palette', 'postgres-databases'], queryFn: listPostgresDatabases, ...lazy })
const mysqlQuery = useQuery({ queryKey: ['palette', 'mysql-databases'], queryFn: listMysqlDatabases, ...lazy })

interface PaletteItem {
  id: string
  group: string
  label: string
  hint?: string
  icon: string
  to: string
  /** Extra text matched by the search besides the label. */
  searchText?: string
}

const navigationItems = computed<PaletteItem[]>(() => {
  const items: PaletteItem[] = []
  for (const feature of featureModules) {
    const navigation = feature.navigation
    if (!navigation) continue
    if (!identity.can(navigation.permission)) continue
    items.push({
      id: `nav-${feature.id}`,
      group: 'Navigation',
      label: navigation.label,
      icon: navigation.icon,
      to: navigation.to,
    })
  }
  return items
})

const resourceItems = computed<PaletteItem[]>(() => {
  const items: PaletteItem[] = []
  for (const site of sitesQuery.data.value ?? []) {
    items.push({
      id: `site-${site.id}`,
      group: 'Sites',
      label: site.displayName,
      hint: site.primaryDomain,
      icon: 'layers',
      to: `/sites/${site.id}`,
      searchText: `${site.displayName} ${site.slug} ${site.primaryDomain}`,
    })
  }
  for (const domain of domainsQuery.data.value ?? []) {
    items.push({
      id: `domain-${domain.id}`,
      group: 'Domains',
      label: domain.hostname,
      hint: domain.kind,
      icon: 'globe',
      to: `/domains?selected=${domain.id}`,
    })
  }
  for (const certificate of certificatesQuery.data.value ?? []) {
    items.push({
      id: `certificate-${certificate.id}`,
      group: 'Certificates',
      label: certificate.primaryDomain,
      hint: certificate.domains.join(', '),
      icon: 'shield',
      to: `/certificates?selected=${certificate.id}`,
      searchText: `${certificate.primaryDomain} ${certificate.domains.join(' ')}`,
    })
  }
  for (const database of postgresQuery.data.value ?? []) {
    items.push({
      id: `postgres-${database.id}`,
      group: 'PostgreSQL',
      label: database.name,
      icon: 'database',
      to: `/databases/${encodeURIComponent(database.id)}`,
    })
  }
  for (const database of mysqlQuery.data.value ?? []) {
    items.push({
      id: `mysql-${database.id}`,
      group: 'MySQL / MariaDB',
      label: database.name,
      icon: 'server',
      to: `/mysql/${encodeURIComponent(database.id)}`,
    })
  }
  return items
})

const RESULTS_PER_GROUP = 8

const results = computed<PaletteItem[]>(() => {
  const query = search.value.trim().toLowerCase()
  if (!query) return navigationItems.value
  const matches = (item: PaletteItem) => (item.searchText ?? item.label).toLowerCase().includes(query)
  const found = navigationItems.value.filter(matches)
  const perGroup = new Map<string, number>()
  for (const item of resourceItems.value) {
    if (!matches(item)) continue
    const count = perGroup.get(item.group) ?? 0
    if (count >= RESULTS_PER_GROUP) continue
    perGroup.set(item.group, count + 1)
    found.push(item)
  }
  return found
})

const groups = computed(() => {
  const ordered: { label: string; entries: PaletteItem[] }[] = []
  for (const item of results.value) {
    const existing = ordered.find((group) => group.label === item.group)
    if (existing) existing.entries.push(item)
    else ordered.push({ label: item.group, entries: [item] })
  }
  return ordered
})

function go(item: PaletteItem) {
  emit('close')
  void router.push(item.to)
}

function onOpenChange(value: boolean) {
  if (!value) emit('close')
}

// Developers cannot read domains, certificates, or databases server-side, so
// the placeholder only promises what their palette can actually surface.
const placeholder = computed(() =>
  identity.user?.role === 'developer' ? 'Search pages and sites…' : 'Search pages, sites, domains, databases…',
)
</script>

<template>
  <DialogRoot :open="open" @update:open="onOpenChange">
    <DialogPortal>
      <DialogOverlay
        class="fixed inset-0 z-[90] bg-canvas/70 backdrop-blur-sm data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0"
      />
      <DialogContent
        class="fixed top-[12vh] left-1/2 z-[90] w-[calc(100%-2rem)] max-w-xl -translate-x-1/2 overflow-hidden rounded-xl border border-outline-strong bg-raised text-ink shadow-[0_24px_60px_-20px_rgba(0,0,0,0.8)] outline-none data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95"
        @open-auto-focus.prevent
      >
        <VisuallyHidden>
          <DialogTitle>Search</DialogTitle>
          <DialogDescription>Search pages, sites, and resources, then press Enter to navigate.</DialogDescription>
        </VisuallyHidden>

        <Combobox :open="true" ignore-filter class="block">
          <div class="flex items-center gap-3 border-b border-outline px-4">
            <AppIcon name="search" :size="16" class="shrink-0 text-ink-muted" />
            <ComboboxInput v-model="search" :placeholder="placeholder" auto-focus />
            <kbd class="shrink-0 rounded border border-outline px-1.5 py-0.5 text-[10px] font-semibold text-ink-muted">Esc</kbd>
          </div>

          <ComboboxContent
            v-if="results.length"
            class="border-none"
            @escape-key-down="emit('close')"
            @pointer-down-outside="emit('close')"
          >
            <ComboboxGroup v-for="group in groups" :key="group.label">
              <ComboboxLabel>{{ group.label }}</ComboboxLabel>
              <ComboboxItem
                v-for="item in group.entries"
                :key="item.id"
                :value="item.id"
                :text-value="item.label"
                class="group"
                @select="go(item)"
              >
                <AppIcon
                  :name="item.icon"
                  :size="15"
                  class="shrink-0 text-ink-muted group-data-[highlighted]:text-accent-300"
                />
                <span class="min-w-0 flex-1 truncate">{{ item.label }}</span>
                <span v-if="item.hint" class="max-w-44 shrink-0 truncate text-xs font-normal text-ink-muted">{{ item.hint }}</span>
              </ComboboxItem>
            </ComboboxGroup>
          </ComboboxContent>

          <div v-else class="p-3">
            <EmptyState icon="search" title="No matches" description="Try a different page name, site, hostname, or database." />
          </div>
        </Combobox>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
