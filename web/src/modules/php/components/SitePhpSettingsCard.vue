<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, reactive, ref, watch } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useCollection } from '@/shared/composables/useCollection'
import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppButton,
  AppCard,
  AppInput,
  JobFailureNotice,
  JobProgress,
  ListToolbar,
  SkeletonRow,
  StatusPill,
  TablePager,
} from '@/shared/ui'

import { getSitePhpSettings, saveSitePhpSettings, type PhpDirective } from '../api'

const props = defineProps<{ siteId: string; phpVersion: string }>()

const identity = useIdentityStore()
const canWrite = computed(() => identity.can('applications.write'))

const settingsQuery = useQuery({
  queryKey: ['php-site-settings', () => props.siteId],
  queryFn: () => getSitePhpSettings(props.siteId),
  enabled: computed(() => !!props.siteId),
  retry: false,
})
const directives = computed(() => settingsQuery.data.value?.items ?? [])
const collection = useCollection<PhpDirective>(() => directives.value, {
  searchText: (d) => `${d.name} ${d.module}`,
  pageSize: 10,
})

const drafts = reactive<Record<string, string>>({})
const resets = reactive<Record<string, boolean>>({})
const editingName = ref('')
const editValue = ref('')
const pendingCount = computed(() => Object.keys(drafts).length + Object.keys(resets).length)

watch(() => props.siteId, clearEdits)

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
  if (editValue.value === directive.value && !directive.managed) delete drafts[directive.name]
  else drafts[directive.name] = editValue.value
  delete resets[directive.name]
  editingName.value = ''
}

function cancelEdit() {
  editingName.value = ''
}

// ↺ discards a pending edit, else clears a site override, else cancels a queued clear.
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
  if (resets[directive.name]) return { label: 'will clear', tone: 'warning' }
  if (drafts[directive.name] !== undefined) return { label: 'edited', tone: 'accent' }
  if (directive.managed) return { label: 'site override', tone: 'info' }
  return undefined
}

const saveRunner = useJobRunner()

async function save() {
  if (!canWrite.value || pendingCount.value === 0 || saveRunner.busy.value) return
  const set = { ...drafts }
  const reset = Object.keys(resets)
  await saveRunner.run(async () => (await saveSitePhpSettings(props.siteId, set, reset)).job.id, {
    onSettled: async () => {
      await settingsQuery.refetch()
    },
    onSuccess: () => {
      clearEdits()
    },
    successToast: 'Per-site PHP settings saved',
    failureMessage: 'Saving per-site PHP settings failed',
  })
}
</script>

<template>
  <AppCard eyebrow="Manage" :title="`PHP settings · PHP ${phpVersion}`">
    <template #actions>
      <AppButton
        size="sm"
        variant="primary"
        icon="check"
        :disabled="!canWrite || pendingCount === 0 || saveRunner.busy.value"
        :loading="saveRunner.busy.value"
        @click="save"
      >
        Save<span v-if="pendingCount"> ({{ pendingCount }})</span>
      </AppButton>
    </template>

    <div class="space-y-4">
      <p class="text-[13px] text-ink-secondary">
        Per-site overrides written to this site's <code class="font-mono text-accent-200">.user.ini</code>. They layer on top
        of the global PHP {{ phpVersion }} settings and apply on the next PHP-FPM refresh. Only directives PHP honours
        per-directory are shown.
      </p>

      <JobFailureNotice v-if="saveRunner.error.value" :message="saveRunner.error.value" :job-id="saveRunner.jobId.value" />
      <JobProgress
        v-if="saveRunner.progress.value"
        :event="saveRunner.progress.value"
        :messages="saveRunner.messages.value"
        :started-at-ms="saveRunner.startedAtMs.value"
      />

      <ListToolbar
        :search="collection.search.value"
        :count="collection.matching.value"
        count-label="directives"
        placeholder="Search directives"
        @update:search="collection.search.value = $event"
      />

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
              <tr v-for="n in 6" :key="n">
                <td colspan="4" class="px-4 py-2"><SkeletonRow /></td>
              </tr>
            </template>
            <tr v-else-if="settingsQuery.isError.value">
              <td colspan="4" class="px-4 py-8 text-center text-ink-muted">Per-site PHP settings couldn't be read.</td>
            </tr>
            <tr v-else-if="!collection.matching.value">
              <td colspan="4" class="px-4 py-8 text-center text-ink-muted">No directives match your search.</td>
            </tr>
            <tr v-for="directive in collection.items.value" v-else :key="directive.name" class="align-top hover:bg-raised/30">
              <td class="px-4 py-3 font-mono text-ink">
                {{ directive.name }}
                <StatusPill v-if="rowTag(directive)" class="ml-2" :tone="rowTag(directive)!.tone" :label="rowTag(directive)!.label" :pulse="false" />
              </td>
              <td class="px-4 py-3 text-ink-muted">{{ directive.module }}</td>
              <td class="px-4 py-3">
                <div v-if="editingName === directive.name" class="flex items-center gap-2">
                  <AppInput v-model="editValue" class="w-48" autofocus @keyup.enter="commitEdit(directive)" @keyup.esc="cancelEdit" />
                  <AppButton size="sm" variant="primary" icon="check" @click="commitEdit(directive)">Set</AppButton>
                  <AppButton size="sm" variant="ghost" @click="cancelEdit">Cancel</AppButton>
                </div>
                <span
                  v-else
                  class="font-mono break-all"
                  :class="resets[directive.name] ? 'text-ink-muted line-through' : drafts[directive.name] !== undefined ? 'text-accent-200' : directive.managed ? 'text-accent-200' : 'text-ink-secondary'"
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
                  :title="resets[directive.name] ? 'Cancel clearing override' : 'Clear site override'"
                  @click="revert(directive)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager
        v-if="collection.matching.value"
        v-model:page="collection.page.value"
        v-model:page-size="collection.pageSize.value"
        :page-count="collection.pageCount.value"
        :total="collection.matching.value"
        :range-start="collection.rangeStart.value"
        :range-end="collection.rangeEnd.value"
        label="directives"
      />
    </div>
  </AppCard>
</template>
