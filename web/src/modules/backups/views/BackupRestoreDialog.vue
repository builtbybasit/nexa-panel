<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, onScopeDispose, reactive, ref, watch, watchEffect } from 'vue'

import { formatBytes, formatDateTime } from '@/shared/formatters'
import { AppAlert, AppButton, AppDialog, AppInput, FormField } from '@/shared/ui'
import { Combobox, ComboboxAnchor, ComboboxEmpty, ComboboxGroup, ComboboxInput, ComboboxItem, ComboboxItemIndicator, ComboboxList, ComboboxTrigger } from '@/shared/ui/combobox'

import { listDatabases } from '../../databases/api'
import { listSites } from '../../sites/api'
import {
  previewBackupRestore,
  type BackupCopy,
  type BackupRestoreChoices,
  type BackupRestorePreview,
  type BackupRestoreRequest,
} from '../api'

const props = defineProps<{ copy: BackupCopy; busy: boolean; failure?: string }>()
const emit = defineEmits<{ close: []; restore: [body: BackupRestoreRequest] }>()

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const databasesQuery = useQuery({ queryKey: ['databases'], queryFn: listDatabases, retry: false })

const siteOptions = computed(() =>
  (sitesQuery.data.value ?? []).map((site) => ({ value: site.id, slug: site.slug, label: `${site.displayName} — ${site.primaryDomain}` })),
)
const postgresOptions = computed(() =>
  (databasesQuery.data.value ?? [])
    .filter((db) => db.engine === 'postgresql')
    .map((db) => ({ value: `postgres:${db.id}`, name: db.name, label: `${db.name} · PostgreSQL` })),
)
const mysqlOptions = computed(() =>
  (databasesQuery.data.value ?? [])
    .filter((db) => db.engine === 'mysql')
    .map((db) => ({ value: `mysql:${db.id}`, name: db.name, label: `${db.name} · MySQL` })),
)
const unverifiedAcknowledged = ref(false)
const confirmation = ref('')
const expectedConfirmation = computed(() => `RESTORE ${props.copy.copyName}`)
const preview = ref<BackupRestorePreview>()
const previewBusy = ref(false)
const previewError = ref('')
let reviewedFingerprint = ''
let expiryTimer: ReturnType<typeof setTimeout> | undefined

// A restorable item derived from one copy entry, plus the reactive destination
// and clear choice the operator will act on.
interface RestoreItem {
  entry: string
  kind: 'site' | 'database' | 'unsupported'
  engine?: 'postgres' | 'mysql' | 'mariadb'
  origin: string // slug or database name the archive came from
  sizeBytes: number
}

const items = computed<RestoreItem[]>(() =>
  props.copy.entries.map((entry) => {
    const site = /^site-([a-z][a-z0-9-]{1,31})\.tar\.gz$/.exec(entry.name)
    if (site) return { entry: entry.name, kind: 'site', origin: site[1] ?? '', sizeBytes: entry.sizeBytes }
    const db = /^db-postgres-([^/\\]+)\.dump$/.exec(entry.name) ?? /^db-(mysql|mariadb)-([^/\\]+)\.sql$/.exec(entry.name)
    if (db) {
      return {
        entry: entry.name,
        kind: 'database',
        engine: entry.name.startsWith('db-postgres-') ? 'postgres' : db[1] as 'mysql' | 'mariadb',
        origin: entry.name.startsWith('db-postgres-') ? db[1] ?? '' : db[2] ?? '',
        sizeBytes: entry.sizeBytes,
      }
    }
    return { entry: entry.name, kind: 'unsupported', origin: entry.name, sizeBytes: entry.sizeBytes }
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
      selections[item.entry] = { include: item.kind !== 'unsupported', dest: defaultDestination(item), clear: false }
    } else if (!existing.dest) {
      existing.dest = defaultDestination(item)
    }
  }
})
function stateFor(item: RestoreItem) {
  return selections[item.entry] ?? { include: false, dest: '', clear: false }
}

const selectionError = computed(() => {
  if (items.value.some((item) => item.kind === 'unsupported')) {
    return 'This copy contains an unsupported entry. Restore is blocked instead of guessing its resource type.'
  }
  if (props.copy.integrityState !== 'passed' && !unverifiedAcknowledged.value) {
    return 'Confirm the integrity risk before restoring this unverified copy.'
  }
  const chosen = items.value.filter((item) => stateFor(item).include)
  if (!chosen.length) return 'Select at least one item to restore.'
  if (chosen.some((item) => !stateFor(item).dest)) return 'Choose a destination for every selected item.'
  return ''
})

const confirmationError = computed(() => {
  if (!preview.value) return ''
  if (confirmation.value !== expectedConfirmation.value) return 'Type the exact restore confirmation shown below.'
  return ''
})

function choices(): BackupRestoreChoices {
  const body: BackupRestoreChoices = {
    sites: [],
    databases: [],
    allowUnverified: props.copy.integrityState !== 'passed' && unverifiedAcknowledged.value,
  }
  for (const item of items.value) {
    const state = stateFor(item)
    if (!state.include) continue
    if (item.kind === 'site') body.sites.push({ entry: item.entry, siteId: state.dest, clear: state.clear })
    else if (item.kind === 'database') body.databases.push({ entry: item.entry, databaseRef: state.dest, clear: state.clear })
  }
  body.sites.sort((left, right) => left.entry.localeCompare(right.entry) || left.siteId.localeCompare(right.siteId))
  body.databases.sort((left, right) => left.entry.localeCompare(right.entry) || left.databaseRef.localeCompare(right.databaseRef))
  return body
}

function choicesFingerprint(body = choices()) {
  return JSON.stringify(body)
}

function clearExpiryTimer() {
  if (expiryTimer !== undefined) clearTimeout(expiryTimer)
  expiryTimer = undefined
}

function invalidatePreview(message = '') {
  clearExpiryTimer()
  preview.value = undefined
  reviewedFingerprint = ''
  confirmation.value = ''
  if (message) previewError.value = message
}

function responseMatchesChoices(response: BackupRestorePreview, body: BackupRestoreChoices) {
  if (
    response.copyId !== props.copy.id || response.copyName !== props.copy.copyName ||
    response.integrityState !== props.copy.integrityState || !response.previewToken
  ) return false
  const expected = [
    ...body.sites.map((item) => ({ sourceEntry: item.entry, destinationRef: item.siteId, clear: item.clear, kind: 'site' })),
    ...body.databases.map((item) => ({ sourceEntry: item.entry, destinationRef: item.databaseRef, clear: item.clear, kind: 'database' })),
  ]
  if (response.items.length !== expected.length) return false
  return expected.every((item) => response.items.some((candidate) =>
    candidate.kind === item.kind && candidate.sourceEntry === item.sourceEntry &&
    candidate.destinationRef === item.destinationRef && candidate.clear === item.clear &&
    Boolean(candidate.destinationLabel) && Boolean(candidate.impact),
  ))
}

async function review() {
  if (selectionError.value || previewBusy.value || props.busy) return
  previewBusy.value = true
  previewError.value = ''
  confirmation.value = ''
  const body = choices()
  const fingerprint = choicesFingerprint(body)
  try {
    const response = await previewBackupRestore(props.copy.id, body)
    const expiresAt = Date.parse(response.expiresAt)
    if (!responseMatchesChoices(response, body) || !Number.isFinite(expiresAt) || expiresAt <= Date.now()) {
      throw new Error('The server returned a stale or mismatched restore preview.')
    }
    preview.value = response
    reviewedFingerprint = fingerprint
    clearExpiryTimer()
    expiryTimer = setTimeout(() => {
      invalidatePreview('The restore preview expired. Review the current plan again.')
    }, Math.min(expiresAt - Date.now(), 2_147_483_647))
  } catch (caught) {
    invalidatePreview(caught instanceof Error ? caught.message : 'The restore preview could not be loaded.')
  } finally {
    previewBusy.value = false
  }
}

function submit() {
  const reviewed = preview.value
  if (!reviewed || selectionError.value || confirmationError.value || props.busy) return
  if (Date.parse(reviewed.expiresAt) <= Date.now()) {
    invalidatePreview('The restore preview expired. Review the current plan again.')
    return
  }
  const body = choices()
  if (choicesFingerprint(body) !== reviewedFingerprint) {
    invalidatePreview('The restore choices changed. Review the current plan again.')
    return
  }
  emit('restore', { ...body, previewToken: reviewed.previewToken } satisfies BackupRestoreRequest)
}

watch(selections, () => {
  if (preview.value) invalidatePreview('The restore choices changed. Review the current plan again.')
}, { deep: true })
watch(unverifiedAcknowledged, () => {
  if (preview.value) invalidatePreview('The integrity acknowledgement changed. Review the current plan again.')
})
watch(() => props.failure, (failure) => {
  if (failure && preview.value) invalidatePreview(`${failure} Review the current plan again.`)
})
onScopeDispose(clearExpiryTimer)
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
          <input
            v-model="stateFor(item).include"
            type="checkbox"
            class="size-4 accent-accent-500"
            :disabled="item.kind === 'unsupported'"
          />
          <span class="font-mono text-[13px]">{{ item.entry }}</span>
          <span class="ml-auto text-xs text-ink-muted">{{ formatBytes(item.sizeBytes) }}</span>
        </label>

        <AppAlert v-if="item.kind === 'unsupported'" tone="danger" class="mt-3">
          <strong>Unsupported backup entry.</strong> Its filename does not identify a managed site or database, so Nexa
          will not guess a destructive restore target.
        </AppAlert>

        <div v-else-if="stateFor(item).include" class="mt-3 grid gap-3 pl-6 sm:grid-cols-2">
          <FormField :label="item.kind === 'site' ? 'Restore to site' : 'Restore to database'">
            <Combobox v-model="stateFor(item).dest">
              <ComboboxAnchor as-child>
                <ComboboxTrigger
                  :label="
                    ((id) =>
                      (item.kind === 'site' ? siteOptions : databaseOptionsFor(item.engine)).find((option) => option.value === id)
                        ?.label ?? '')(stateFor(item).dest)
                  "
                />
              </ComboboxAnchor>
              <ComboboxList>
                <ComboboxInput placeholder="Search…" />
                <ComboboxEmpty>Nothing available</ComboboxEmpty>
                <ComboboxGroup>
                  <template v-if="item.kind === 'site'">
                    <ComboboxItem v-for="option in siteOptions" :key="option.value" :value="option.value" :text-value="option.label">
                      {{ option.label }}<ComboboxItemIndicator />
                    </ComboboxItem>
                  </template>
                  <template v-else>
                    <ComboboxItem
                      v-for="option in databaseOptionsFor(item.engine)"
                      :key="option.value"
                      :value="option.value"
                      :text-value="option.label"
                    >
                      {{ option.label }}<ComboboxItemIndicator />
                    </ComboboxItem>
                  </template>
                </ComboboxGroup>
              </ComboboxList>
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

      <section v-if="preview" class="space-y-3 rounded-xl border border-accent-400/30 bg-accent-400/[0.05] p-4" aria-label="Reviewed restore plan">
        <div class="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h3 class="text-sm font-semibold text-ink">Reviewed restore plan</h3>
            <p class="mt-0.5 text-xs text-ink-muted">Bound to these destinations until {{ formatDateTime(preview.expiresAt) }}.</p>
          </div>
          <span class="rounded-full border border-accent-400/30 px-2 py-0.5 text-[11px] font-medium text-accent-200">
            Server verified
          </span>
        </div>

        <AppAlert v-for="warning in preview.warnings" :key="warning" tone="warning">{{ warning }}</AppAlert>

        <ul class="space-y-2">
          <li v-for="item in preview.items" :key="`${item.kind}:${item.sourceEntry}`" class="rounded-lg border border-outline bg-canvas/50 p-3">
            <div class="flex flex-wrap items-center gap-2 text-[13px]">
              <span class="font-mono text-ink">{{ item.sourceEntry }}</span>
              <span class="text-ink-muted" aria-hidden="true">→</span>
              <strong class="text-ink">{{ item.destinationLabel }}</strong>
              <span class="font-mono text-[11px] text-ink-muted">{{ item.destinationRef }}</span>
              <span
                class="rounded-full px-2 py-0.5 text-[11px] font-medium"
                :class="item.clear ? 'bg-rose-400/10 text-rose-200' : 'bg-white/[0.05] text-ink-secondary'"
              >
                {{ item.clear ? 'Clear first' : 'Keep unrelated data' }}
              </span>
            </div>
            <p class="mt-1.5 text-xs leading-relaxed text-ink-secondary">{{ item.impact }}</p>
          </li>
        </ul>

        <FormField :label="`Type ${expectedConfirmation} to confirm`">
          <AppInput
            v-model="confirmation"
            autocomplete="off"
            spellcheck="false"
            class="font-mono"
            :placeholder="expectedConfirmation"
          />
        </FormField>
        <AppAlert v-if="confirmationError" tone="danger">{{ confirmationError }}</AppAlert>
      </section>

      <AppAlert v-if="selectionError" tone="danger">{{ selectionError }}</AppAlert>
      <AppAlert v-if="previewError" tone="danger">{{ previewError }}</AppAlert>
    </div>

    <template #footer>
      <AppButton :disabled="busy" @click="emit('close')">Cancel</AppButton>
      <AppButton
        v-if="!preview"
        variant="primary"
        icon="scan"
        :loading="previewBusy"
        :disabled="Boolean(selectionError) || busy"
        @click="review"
      >
        Review restore
      </AppButton>
      <AppButton v-else variant="danger" icon="rotate-ccw" :loading="busy" :disabled="Boolean(confirmationError)" @click="submit">
        Restore
      </AppButton>
    </template>
  </AppDialog>
</template>
