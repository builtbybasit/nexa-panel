<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, reactive, ref, watch } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppInput,
  EmptyState,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  PageHeader,
  SkeletonRow,
  StatusPill,
  TablePager,
} from '@/shared/ui'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'

import {
  changePhpExtension,
  listPhpExtensions,
  listPhpSettings,
  listPhpVersions,
  savePhpSettings,
  type PhpDirective,
  type PhpExtension,
} from '../api'

const identity = useIdentityStore()
const canWrite = computed(() => identity.can('applications.write'))

const versionsQuery = useQuery({ queryKey: ['php-versions'], queryFn: listPhpVersions, retry: false })
const versions = computed(() => versionsQuery.data.value ?? [])
const selectedVersion = ref('')
watch(
  versions,
  (list) => {
    if (list.length && !list.includes(selectedVersion.value)) selectedVersion.value = list[0] ?? ''
  },
  { immediate: true },
)

const tab = ref<'modules' | 'settings'>('modules')

// --- PHP modules (extensions) ---
const extensionsQuery = useQuery({
  queryKey: ['php-extensions', selectedVersion],
  queryFn: () => listPhpExtensions(selectedVersion.value),
  enabled: computed(() => tab.value === 'modules' && !!selectedVersion.value),
  retry: false,
})
const extensions = computed(() => extensionsQuery.data.value ?? [])
const extCollection = useCollection<PhpExtension>(() => extensions.value, { searchText: (e) => e.name, pageSize: 10 })

const extRunner = useJobRunner()
const pendingExt = ref('')

async function changeExtension(extension: PhpExtension, action: 'install' | 'remove') {
  if (!canWrite.value || extRunner.busy.value) return
  pendingExt.value = extension.name
  await extRunner.run(async () => (await changePhpExtension(selectedVersion.value, extension.name, action)).job.id, {
    onSettled: async () => {
      await extensionsQuery.refetch()
    },
    successToast: `php${selectedVersion.value}-${extension.name} ${action === 'remove' ? 'removed' : 'installed'}`,
    failureMessage: `The ${extension.name} extension ${action} failed`,
  })
  pendingExt.value = ''
}

// --- PHP settings (php.ini directives) ---
const settingsQuery = useQuery({
  queryKey: ['php-settings', selectedVersion],
  queryFn: () => listPhpSettings(selectedVersion.value),
  enabled: computed(() => tab.value === 'settings' && !!selectedVersion.value),
  retry: false,
})
const directives = computed(() => settingsQuery.data.value ?? [])
const setCollection = useCollection<PhpDirective>(() => directives.value, {
  searchText: (d) => `${d.name} ${d.module}`,
  pageSize: 10,
})

// Pending edits. `drafts` holds directives being set to a new value; `resets`
// holds directives to revert to their PHP default (dropped from nexa's drop-in).
const drafts = reactive<Record<string, string>>({})
const resets = reactive<Record<string, boolean>>({})
const editingName = ref('')
const editValue = ref('')
const pendingCount = computed(() => Object.keys(drafts).length + Object.keys(resets).length)

// Switching version discards unsaved edits — they belong to the old branch.
watch(selectedVersion, clearEdits)

function clearEdits() {
  for (const key of Object.keys(drafts)) delete drafts[key]
  for (const key of Object.keys(resets)) delete resets[key]
  editingName.value = ''
}

function beginEdit(directive: PhpDirective) {
  if (!canWrite.value) return
  editingName.value = directive.name
  editValue.value = drafts[directive.name] ?? directive.value
}

function commitEdit(directive: PhpDirective) {
  if (editValue.value === directive.value) delete drafts[directive.name]
  else drafts[directive.name] = editValue.value
  delete resets[directive.name]
  editingName.value = ''
}

function cancelEdit() {
  editingName.value = ''
}

// The reset (↺) button cycles: discard a pending edit, else queue a reset for a
// managed directive, else (already queued) cancel that reset.
function revert(directive: PhpDirective) {
  if (!canWrite.value) return
  if (resets[directive.name]) {
    delete resets[directive.name]
    return
  }
  if (drafts[directive.name] !== undefined) {
    delete drafts[directive.name]
    if (editingName.value === directive.name) editingName.value = ''
    return
  }
  if (directive.managed) resets[directive.name] = true
}

function canRevert(directive: PhpDirective) {
  return directive.managed || drafts[directive.name] !== undefined || resets[directive.name]
}

function rowValue(directive: PhpDirective) {
  return drafts[directive.name] ?? directive.value
}

function rowTag(directive: PhpDirective): { label: string; tone: 'accent' | 'warning' | 'info' } | undefined {
  if (resets[directive.name]) return { label: 'will reset', tone: 'warning' }
  if (drafts[directive.name] !== undefined) return { label: 'edited', tone: 'accent' }
  if (directive.managed) return { label: 'overridden', tone: 'info' }
  return undefined
}

const saveRunner = useJobRunner()

async function save() {
  if (!canWrite.value || pendingCount.value === 0 || saveRunner.busy.value) return
  const set = { ...drafts }
  const reset = Object.keys(resets)
  await saveRunner.run(async () => (await savePhpSettings(selectedVersion.value, set, reset)).job.id, {
    onSettled: async () => {
      await settingsQuery.refetch()
    },
    onSuccess: () => {
      clearEdits()
    },
    successToast: `PHP ${selectedVersion.value} settings saved`,
    failureMessage: 'Saving PHP settings failed',
  })
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Runtime configuration"
      title="PHP"
      description="Install PHP extensions and edit php.ini directives for each installed PHP version. Changes are applied per-version through reviewed, agent-signed plans and take effect on a PHP-FPM reload."
    >
      <StatusPill tone="accent" label="per-version" :pulse="false" />
    </PageHeader>

    <JobFailureNotice v-if="extRunner.error.value" :message="extRunner.error.value" :job-id="extRunner.jobId.value" />
    <JobFailureNotice v-if="saveRunner.error.value" :message="saveRunner.error.value" :job-id="saveRunner.jobId.value" />
    <JobProgress
      v-if="extRunner.progress.value"
      :event="extRunner.progress.value"
      :messages="extRunner.messages.value"
      :started-at-ms="extRunner.startedAtMs.value"
    />
    <JobProgress
      v-if="saveRunner.progress.value"
      :event="saveRunner.progress.value"
      :messages="saveRunner.messages.value"
      :started-at-ms="saveRunner.startedAtMs.value"
    />

    <AppAlert v-if="versionsQuery.isError.value" tone="danger">
      Installed PHP versions couldn't be loaded. The node agent may be unreachable.
    </AppAlert>
    <EmptyState
      v-else-if="!versionsQuery.isPending.value && !versions.length"
      icon="wrench"
      title="No PHP versions installed"
      description="Install a PHP version from the Applications page first, then manage its extensions and settings here."
    />

    <template v-else>
      <!-- Tab bar + version selector -->
      <div class="flex flex-wrap items-center justify-between gap-4 border-b border-outline">
        <div class="flex gap-6">
          <button
            type="button"
            class="border-b-2 pb-3 text-sm font-semibold transition-colors"
            :class="tab === 'modules' ? 'border-accent-500 text-accent-200' : 'border-transparent text-ink-muted hover:text-ink-secondary'"
            @click="tab = 'modules'"
          >
            PHP modules
          </button>
          <button
            type="button"
            class="border-b-2 pb-3 text-sm font-semibold transition-colors"
            :class="tab === 'settings' ? 'border-accent-500 text-accent-200' : 'border-transparent text-ink-muted hover:text-ink-secondary'"
            @click="tab = 'settings'"
          >
            PHP settings
          </button>
        </div>
        <label class="flex items-center gap-2 pb-2 text-xs font-semibold text-ink-muted">
          PHP version
          <Select v-model="selectedVersion">
            <SelectTrigger class="w-32" />
            <SelectContent>
              <SelectItem v-for="version in versions" :key="version" :value="version">PHP {{ version }}</SelectItem>
            </SelectContent>
          </Select>
        </label>
      </div>

      <!-- PHP modules (extensions) -->
      <div v-if="tab === 'modules'" class="space-y-4">
        <ListToolbar
          :search="extCollection.search.value"
          :count="extCollection.matching.value"
          count-label="extensions"
          placeholder="Search extensions"
          @update:search="extCollection.search.value = $event"
        />

        <div class="overflow-hidden rounded-xl border border-outline">
          <table class="w-full text-sm">
            <thead class="bg-raised/50 text-left text-[11px] font-semibold tracking-[0.1em] text-ink-muted uppercase">
              <tr>
                <th class="px-4 py-3">Name</th>
                <th class="px-4 py-3">Status</th>
                <th class="w-40 px-4 py-3 text-right">Action</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-outline">
              <template v-if="extensionsQuery.isPending.value">
                <tr v-for="n in 6" :key="n">
                  <td colspan="3" class="px-4 py-2"><SkeletonRow /></td>
                </tr>
              </template>
              <tr v-else-if="!extCollection.matching.value">
                <td colspan="3" class="px-4 py-10 text-center text-ink-muted">No extensions match your search.</td>
              </tr>
              <tr v-for="extension in extCollection.items.value" v-else :key="extension.name" class="hover:bg-raised/30">
                <td class="px-4 py-3 font-mono text-ink">php-{{ extension.name }}</td>
                <td class="px-4 py-3">
                  <StatusPill
                    :tone="extension.installed ? 'success' : 'neutral'"
                    :label="extension.installed ? 'Installed' : 'Not installed'"
                    :pulse="false"
                  />
                </td>
                <td class="px-4 py-3 text-right">
                  <AppButton
                    v-if="!extension.installed"
                    size="sm"
                    variant="primary"
                    icon="download"
                    :disabled="!canWrite || extRunner.busy.value"
                    :loading="pendingExt === extension.name"
                    @click="changeExtension(extension, 'install')"
                  >
                    Install
                  </AppButton>
                  <AppButton
                    v-else
                    size="sm"
                    variant="danger"
                    icon="trash"
                    :disabled="!canWrite || extRunner.busy.value"
                    :loading="pendingExt === extension.name"
                    @click="changeExtension(extension, 'remove')"
                  >
                    Uninstall
                  </AppButton>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <TablePager
          v-if="extCollection.matching.value"
          v-model:page="extCollection.page.value"
          v-model:page-size="extCollection.pageSize.value"
          :page-count="extCollection.pageCount.value"
          :total="extCollection.matching.value"
          :range-start="extCollection.rangeStart.value"
          :range-end="extCollection.rangeEnd.value"
          label="extensions"
        />
      </div>

      <!-- PHP settings (php.ini directives) -->
      <div v-else class="space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <ListToolbar
            class="flex-1"
            :search="setCollection.search.value"
            :count="setCollection.matching.value"
            count-label="directives"
            placeholder="Search directives"
            @update:search="setCollection.search.value = $event"
          />
          <AppButton
            variant="primary"
            icon="check"
            :disabled="!canWrite || pendingCount === 0 || saveRunner.busy.value"
            :loading="saveRunner.busy.value"
            @click="save"
          >
            Save<span v-if="pendingCount"> ({{ pendingCount }})</span>
          </AppButton>
        </div>

        <div class="overflow-hidden rounded-xl border border-outline">
          <table class="w-full text-sm">
            <thead class="bg-raised/50 text-left text-[11px] font-semibold tracking-[0.1em] text-ink-muted uppercase">
              <tr>
                <th class="px-4 py-3">Name</th>
                <th class="px-4 py-3">Module</th>
                <th class="px-4 py-3">Value</th>
                <th class="w-24 px-4 py-3 text-right">Edit</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-outline">
              <template v-if="settingsQuery.isPending.value">
                <tr v-for="n in 8" :key="n">
                  <td colspan="4" class="px-4 py-2"><SkeletonRow /></td>
                </tr>
              </template>
              <tr v-else-if="settingsQuery.isError.value">
                <td colspan="4" class="px-4 py-10 text-center text-ink-muted">
                  Settings couldn't be read. The {{ `php${selectedVersion}` }} CLI must be installed on the node.
                </td>
              </tr>
              <tr v-else-if="!setCollection.matching.value">
                <td colspan="4" class="px-4 py-10 text-center text-ink-muted">No directives match your search.</td>
              </tr>
              <tr v-for="directive in setCollection.items.value" v-else :key="directive.name" class="align-top hover:bg-raised/30">
                <td class="px-4 py-3 font-mono text-ink">
                  {{ directive.name }}
                  <StatusPill v-if="rowTag(directive)" class="ml-2" :tone="rowTag(directive)!.tone" :label="rowTag(directive)!.label" :pulse="false" />
                </td>
                <td class="px-4 py-3 text-ink-muted">{{ directive.module }}</td>
                <td class="px-4 py-3">
                  <div v-if="editingName === directive.name" class="flex items-center gap-2">
                    <AppInput
                      v-model="editValue"
                      class="w-56"
                      autofocus
                      @keyup.enter="commitEdit(directive)"
                      @keyup.esc="cancelEdit"
                    />
                    <AppButton size="sm" variant="primary" icon="check" @click="commitEdit(directive)">Set</AppButton>
                    <AppButton size="sm" variant="ghost" @click="cancelEdit">Cancel</AppButton>
                  </div>
                  <span
                    v-else
                    class="font-mono break-all"
                    :class="resets[directive.name] ? 'text-ink-muted line-through' : drafts[directive.name] !== undefined ? 'text-accent-200' : 'text-ink-secondary'"
                  >
                    {{ rowValue(directive) || '—' }}
                  </span>
                </td>
                <td class="px-4 py-3 text-right whitespace-nowrap">
                  <AppButton
                    size="sm"
                    variant="ghost"
                    icon="pencil"
                    :disabled="!canWrite || editingName === directive.name"
                    @click="beginEdit(directive)"
                  />
                  <AppButton
                    size="sm"
                    variant="ghost"
                    icon="rotate-ccw"
                    :disabled="!canWrite || !canRevert(directive)"
                    :title="resets[directive.name] ? 'Cancel reset' : 'Reset to default'"
                    @click="revert(directive)"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <TablePager
          v-if="setCollection.matching.value"
          v-model:page="setCollection.page.value"
          v-model:page-size="setCollection.pageSize.value"
          :page-count="setCollection.pageCount.value"
          :total="setCollection.matching.value"
          :range-start="setCollection.rangeStart.value"
          :range-end="setCollection.rangeEnd.value"
          label="directives"
        />
      </div>
    </template>
  </section>
</template>
