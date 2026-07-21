<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, reactive, ref, watchEffect } from 'vue'

import { formatBytes } from '@/shared/formatters'
import { AppAlert, AppButton, AppDialog, AppInput, FormField } from '@/shared/ui'
import { Combobox, ComboboxEmpty, ComboboxFloatingContent, ComboboxSelectItem, ComboboxTriggerInput } from '@/shared/ui/combobox'

import { listDatabases as listPostgresDatabases } from '../../databases/api'
import { listDatabases as listMysqlDatabases } from '../../mysql/api'
import { listSites } from '../../sites/api'
import type { BackupCopy, BackupRestoreRequest } from '../api'

const props = defineProps<{ copy: BackupCopy; busy: boolean }>()
const emit = defineEmits<{ close: []; restore: [body: BackupRestoreRequest] }>()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const pgQuery = useQuery({ queryKey: ['postgres-databases'], queryFn: listPostgresDatabases, retry: false })
const myQuery = useQuery({ queryKey: ['mysql-databases'], queryFn: listMysqlDatabases, retry: false })

const siteOptions = computed(() =>
  (sitesQuery.data.value ?? []).map((site) => ({ value: site.id, slug: site.slug, label: `${site.displayName} — ${site.primaryDomain}` })),
)
const postgresOptions = computed(() =>
  (pgQuery.data.value ?? []).map((db) => ({ value: `postgres:${db.id}`, name: db.name, label: `${db.name} · PostgreSQL` })),
)
const mysqlOptions = computed(() =>
  (myQuery.data.value ?? []).map((db) => ({ value: `mysql:${db.id}`, name: db.name, label: `${db.name} · MySQL` })),
)
const unverifiedAcknowledged = ref(false)

// A restorable item derived from one copy entry, plus the reactive destination
// and clear choice the operator will act on.
interface RestoreItem {
  entry: string
  kind: 'site' | 'database'
  engine?: 'postgres' | 'mysql' | 'mariadb'
  origin: string // slug or database name the archive came from
  sizeBytes: number
}

const items = computed<RestoreItem[]>(() =>
  props.copy.entries.map((entry) => {
    const site = /^site-(.+)\.tar\.gz$/.exec(entry.name)
    if (site) return { entry: entry.name, kind: 'site', origin: site[1] ?? '', sizeBytes: entry.sizeBytes }
    const db = /^db-(postgres|mysql|mariadb)-(.+)\.(?:dump|sql)$/.exec(entry.name)
    if (db) {
      return {
        entry: entry.name,
        kind: 'database',
        engine: db[1] as 'postgres' | 'mysql' | 'mariadb',
        origin: db[2] ?? '',
        sizeBytes: entry.sizeBytes,
      }
    }
    return { entry: entry.name, kind: 'site', origin: entry.name, sizeBytes: entry.sizeBytes }
  }),
)

// Destination candidates for a database entry: PostgreSQL entries can only land
// in a PostgreSQL database; mysql/mariadb entries land in a MySQL-family one.
function databaseOptionsFor(engine?: string) {
  return engine === 'postgres' ? postgresOptions.value : mysqlOptions.value
}

function defaultDestination(item: RestoreItem): string {
  if (item.kind === 'site') {
    return siteOptions.value.find((option) => option.slug === item.origin)?.value ?? ''
  }
  return databaseOptionsFor(item.engine).find((option) => option.name === item.origin)?.value ?? ''
}

// Per-entry form state, keyed by entry name. Populated eagerly (and defaults
// filled once the destination lists load) so template/computed only ever read.
const selections = reactive<Record<string, { include: boolean; dest: string; clear: boolean }>>({})
watchEffect(() => {
  for (const item of items.value) {
    const existing = selections[item.entry]
    if (!existing) {
      selections[item.entry] = { include: true, dest: defaultDestination(item), clear: false }
    } else if (!existing.dest) {
      existing.dest = defaultDestination(item)
    }
  }
})
function stateFor(item: RestoreItem) {
  return selections[item.entry] ?? { include: false, dest: '', clear: false }
}

const error = computed(() => {
  if (props.copy.integrityState !== 'passed' && !unverifiedAcknowledged.value) {
    return 'Confirm the integrity risk before restoring this unverified copy.'
  }
  const chosen = items.value.filter((item) => stateFor(item).include)
  if (!chosen.length) return 'Select at least one item to restore.'
  if (chosen.some((item) => !stateFor(item).dest)) return 'Choose a destination for every selected item.'
  return ''
})

function submit() {
  if (error.value || props.busy) return
  const body: BackupRestoreRequest = {
    sites: [],
    databases: [],
    allowUnverified: props.copy.integrityState !== 'passed' && unverifiedAcknowledged.value,
  }
  for (const item of items.value) {
    const state = stateFor(item)
    if (!state.include) continue
    if (item.kind === 'site') body.sites.push({ entry: item.entry, siteId: state.dest, clear: state.clear })
    else body.databases.push({ entry: item.entry, databaseRef: state.dest, clear: state.clear })
  }
  emit('restore', body)
}
</script>

<template>
  <AppDialog :open="true" :title="`Restore ${copy.copyName}`" size="lg" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-[13px] text-ink-secondary">
        Choose where each item in this copy is restored. Restoring overwrites files and databases at the destination —
        enable <strong>Clear first</strong> to remove existing data before the copy is applied.
      </p>

      <AppAlert v-if="!copy.healthy" tone="warning">
        This copy has not passed both integrity and restore verification. Continue only if the recovery need outweighs that risk.
      </AppAlert>
      <label
        v-if="copy.integrityState !== 'passed'"
        class="flex items-start gap-2.5 rounded-lg border border-amber-400/25 bg-amber-400/[0.05] p-3 text-[13px] text-amber-100"
      >
        <input v-model="unverifiedAcknowledged" type="checkbox" class="mt-0.5 size-4 accent-amber-400" />
        I understand that this copy has no passing integrity check and still want to restore it.
      </label>

      <div v-for="item in items" :key="item.entry" class="rounded-xl border border-outline bg-white/[0.02] p-3">
        <label class="flex items-center gap-2.5 text-sm text-ink">
          <input type="checkbox" class="size-4 accent-accent-500" v-model="stateFor(item).include" />
          <span class="font-mono text-[13px]">{{ item.entry }}</span>
          <span class="ml-auto text-xs text-ink-muted">{{ formatBytes(item.sizeBytes) }}</span>
        </label>

        <div v-if="stateFor(item).include" class="mt-3 grid gap-3 pl-6 sm:grid-cols-2">
          <FormField :label="item.kind === 'site' ? 'Restore to site' : 'Restore to database'">
            <Combobox v-model="stateFor(item).dest">
              <ComboboxTriggerInput
                :display-value="
                  (id) =>
                    (item.kind === 'site' ? siteOptions : databaseOptionsFor(item.engine)).find((option) => option.value === id)
                      ?.label ?? ''
                "
              />
              <ComboboxFloatingContent>
                <ComboboxEmpty>Nothing available</ComboboxEmpty>
                <template v-if="item.kind === 'site'">
                  <ComboboxSelectItem v-for="option in siteOptions" :key="option.value" :value="option.value" :text-value="option.label">
                    {{ option.label }}
                  </ComboboxSelectItem>
                </template>
                <template v-else>
                  <ComboboxSelectItem
                    v-for="option in databaseOptionsFor(item.engine)"
                    :key="option.value"
                    :value="option.value"
                    :text-value="option.label"
                  >
                    {{ option.label }}
                  </ComboboxSelectItem>
                </template>
              </ComboboxFloatingContent>
            </Combobox>
          </FormField>
          <FormField label="From" hint="The item's origin in this copy.">
            <AppInput :model-value="item.origin" disabled />
          </FormField>
          <label class="flex items-center gap-2.5 text-[13px] text-ink-secondary sm:col-span-2">
            <input type="checkbox" class="size-4 accent-rose-500" v-model="stateFor(item).clear" />
            Clear the destination's current data first
          </label>
        </div>
      </div>

      <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>
    </div>

    <template #footer>
      <AppButton :disabled="busy" @click="emit('close')">Cancel</AppButton>
      <AppButton variant="primary" icon="rotate-ccw" :loading="busy" :disabled="Boolean(error)" @click="submit">
        Restore
      </AppButton>
    </template>
  </AppDialog>
</template>
