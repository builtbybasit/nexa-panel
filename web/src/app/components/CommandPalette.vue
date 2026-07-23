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
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/shared/ui/command'

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
const lazy = { retry: false, staleTime: 60_000 } as const
const sitesQuery = useQuery({
  queryKey: ['palette', 'sites'],
  queryFn: listSites,
  enabled: computed(() => hasOpened.value && identity.can('sites.read')),
  ...lazy,
})
const domainsQuery = useQuery({
  queryKey: ['palette', 'domains'],
  queryFn: () => listDomains(),
  enabled: computed(() => hasOpened.value && identity.can('domains.read')),
  ...lazy,
})
const certificatesQuery = useQuery({
  queryKey: ['palette', 'certificates'],
  queryFn: () => listCertificates(),
  enabled: computed(() => hasOpened.value && identity.can('certificates.read')),
  ...lazy,
})
const postgresQuery = useQuery({
  queryKey: ['palette', 'postgres-databases'],
  queryFn: listPostgresDatabases,
  enabled: computed(() => hasOpened.value && identity.can('databases.read')),
  ...lazy,
})
const mysqlQuery = useQuery({
  queryKey: ['palette', 'mysql-databases'],
  queryFn: listMysqlDatabases,
  enabled: computed(() => hasOpened.value && identity.can('databases.read')),
  ...lazy,
})

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

// The Command menu filters by each row's `text-value`; grouping is purely for
// display. Resource groups are only rendered while searching (see the template)
// so the empty state stays limited to navigation.
const resourceGroups = computed(() => {
  const ordered: { label: string; entries: PaletteItem[] }[] = []
  for (const item of resourceItems.value) {
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

        <Command v-model:search-term="search" @keydown.escape="emit('close')">
          <CommandInput :placeholder="placeholder">
            <template #suffix>
              <kbd class="shrink-0 rounded border border-outline px-1.5 py-0.5 text-[10px] font-semibold text-ink-muted">Esc</kbd>
            </template>
          </CommandInput>

          <CommandList>
            <CommandEmpty>
              <EmptyState icon="search" title="No matches" description="Try a different page name, site, hostname, or database." />
            </CommandEmpty>

            <CommandGroup heading="Navigation">
              <CommandItem
                v-for="item in navigationItems"
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
              </CommandItem>
            </CommandGroup>

            <!--
              Resource rows stay mounted so the Command filter has them registered
              from the first keystroke; `v-show` merely hides them until searching,
              keeping the resting palette limited to navigation.
            -->
            <div v-show="search.trim()" role="presentation">
              <CommandGroup v-for="group in resourceGroups" :key="group.label" :heading="group.label">
                <CommandItem
                  v-for="item in group.entries"
                  :key="item.id"
                  :value="item.id"
                  :text-value="item.searchText ?? item.label"
                  :disabled="!search.trim()"
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
                </CommandItem>
              </CommandGroup>
            </div>
          </CommandList>
        </Command>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
