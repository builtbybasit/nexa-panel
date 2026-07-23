<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, defineAsyncComponent, ref, watch } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useToasts } from '@/shared/composables/useToasts'
import { formatBytes, formatDateTime } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, SkeletonRow, StatusPill } from '@/shared/ui'

import { getSharedEnv, SHARED_ENV_MAX_BYTES, updateSharedEnv } from '../api'

// Lazily imported for the reason the file manager imports it lazily: the editor
// and its language workers are a large bundle, and most visits to this page
// never open them.
const MonacoEditor = defineAsyncComponent(() => import('@/shared/ui/MonacoEditor.vue'))

const props = defineProps<{ siteId: string }>()

const identity = useIdentityStore()
const { push } = useToasts()
const canWrite = computed(() => identity.can('deploy.write'))

const envQuery = useQuery({
  queryKey: ['shared-env', () => props.siteId],
  queryFn: () => getSharedEnv(props.siteId),
  enabled: computed(() => !!props.siteId),
  retry: false,
})
const shared = computed(() => envQuery.data.value)

// The editable draft is detached from the query cache, and a refetch only
// re-seeds it while nothing is pending — a settled read must not clobber edits.
const draft = ref('')
const saving = ref(false)
const saveError = ref('')
// seeded is what separates "the user has not typed yet" from "the draft happens
// to differ from the server". Before the first fill the draft is the empty
// string, so `dirty` is spuriously true against any non-empty document — using
// it alone as the clobber guard would leave the editor blank, and reading
// "Unsaved changes", on exactly the mount this guard is meant to serve.
const seeded = ref(false)

// Declared before the watcher below on purpose: the watcher is `immediate`, so
// its callback runs synchronously inside setup() — and vue-query seeds a
// query's data from the cache synchronously, so on a revisit within gcTime the
// callback sees data and reads these refs. Declaring them after the watch would
// read them inside their temporal dead zone and throw, blanking the page.
const dirty = computed(() => seeded.value && !!shared.value && draft.value !== shared.value.content)

watch(
  shared,
  (current) => {
    if (!current) return
    // A settled read re-seeds the draft only while nothing is pending: it must
    // never clobber edits the user has not saved yet.
    if (seeded.value && dirty.value) return
    draft.value = current.content
    seeded.value = true
  },
  { immediate: true },
)

// The document is measured in bytes, not characters: the cap the panel and the
// node both enforce is a byte cap, and a UTF-8 value spends more than one.
const bytes = computed(() => new TextEncoder().encode(draft.value).length)
const tooLarge = computed(() => bytes.value > SHARED_ENV_MAX_BYTES)

async function save() {
  if (!canWrite.value || saving.value || !dirty.value) return
  if (tooLarge.value) {
    saveError.value = `The file must be at most ${formatBytes(SHARED_ENV_MAX_BYTES)}.`
    return
  }
  saving.value = true
  saveError.value = ''
  try {
    await updateSharedEnv(props.siteId, draft.value)
    await envQuery.refetch()
    push({ tone: 'success', title: 'Shared .env saved' })
  } catch (caught) {
    saveError.value = caught instanceof Error ? caught.message : 'The file could not be saved.'
  } finally {
    saving.value = false
  }
}

function revert() {
  draft.value = shared.value?.content ?? ''
  saveError.value = ''
}
</script>

<template>
  <AppCard eyebrow="Configuration" title="Shared environment">
    <template #actions>
      <StatusPill
        v-if="shared"
        :tone="shared.present ? 'accent' : 'info'"
        :label="shared.present ? 'In place' : 'Not created yet'"
        :pulse="false"
      />
    </template>

    <div class="space-y-4">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        This file survives every release: the deployer links it into each new release as the application's
        <code class="font-mono">.env</code>. It holds credentials, so the panel never logs it, never puts it in a job,
        and records only its size and checksum in the audit trail.
      </p>

      <AppAlert v-if="saveError" tone="danger">{{ saveError }}</AppAlert>

      <div v-if="envQuery.isPending.value" class="space-y-1" aria-hidden="true">
        <SkeletonRow v-for="n in 3" :key="n" />
      </div>
      <AppAlert v-else-if="envQuery.isError.value" tone="danger">
        <p>{{ (envQuery.error.value as Error | null)?.message ?? 'The shared environment file could not be loaded.' }}</p>
        <AppButton size="sm" class="mt-2" @click="envQuery.refetch()">Retry</AppButton>
      </AppAlert>

      <template v-else-if="shared">
        <p class="font-mono text-[12px] break-all text-ink-muted">{{ shared.path }}</p>

        <div class="h-80 overflow-hidden rounded-xl border border-outline">
          <MonacoEditor
            v-model="draft"
            language="ini"
            :minimap="false"
            :readonly="!canWrite || saving"
            @save="save"
          />
        </div>

        <div class="flex flex-wrap items-center gap-3 text-[12px] text-ink-muted">
          <span :class="tooLarge ? 'text-rose-300' : ''">
            {{ formatBytes(bytes) }} of {{ formatBytes(SHARED_ENV_MAX_BYTES) }}
          </span>
          <span v-if="shared.modifiedAt">Last changed {{ formatDateTime(shared.modifiedAt) }}</span>
          <span v-if="dirty" class="text-amber-300">Unsaved changes</span>
        </div>

        <template v-if="canWrite">
          <div class="flex flex-wrap gap-3 border-t border-outline pt-4">
            <AppButton
              variant="primary"
              icon="check"
              :disabled="!dirty || tooLarge"
              :loading="saving"
              @click="save"
            >
              Save
            </AppButton>
            <AppButton variant="ghost" icon="rotate-cw" :disabled="!dirty || saving" @click="revert">
              Discard changes
            </AppButton>
          </div>
        </template>
        <AppAlert v-else tone="info">An administrator is required to change this file.</AppAlert>
      </template>
    </div>
  </AppCard>
</template>
