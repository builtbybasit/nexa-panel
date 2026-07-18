<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, onBeforeUnmount, ref } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import { AppAlert, AppButton, AppDialog, AppIcon, AppInput, AppSelect, EmptyState, FormField, Switch } from '@/shared/ui'

import { listDatabases as listPostgresDatabases } from '../../databases/api'
import { listDatabases as listMysqlDatabases } from '../../mysql/api'
import { describe as describeCron, nextRuns, normalize } from '../../schedules/cron'
import { listSites } from '../../sites/api'
import {
  createBackupPlan,
  updateBackupPlan,
  type BackupAccount,
  type BackupPlan,
  type BackupPlanRequest,
} from '../api'

const props = defineProps<{ plan?: BackupPlan | undefined; accounts: BackupAccount[] }>()
const emit = defineEmits<{ close: []; saved: [] }>()

const presets = [
  { label: 'Daily at midnight', value: '0 0 * * *' },
  { label: 'Daily at 03:00', value: '0 3 * * *' },
  { label: 'Weekly (Sunday 03:00)', value: '0 3 * * 0' },
  { label: 'Monthly (1st at 03:00)', value: '0 3 1 * *' },
]

const name = ref(props.plan?.name ?? '')
const accountId = ref(props.plan?.accountId ?? props.accounts[0]?.id ?? '')
const copiesLimit = ref<string | number>(props.plan?.copiesLimit ?? 10)
const cronExpression = ref(props.plan?.schedule ?? '0 3 * * *')
const enabled = ref(props.plan?.enabled ?? true)
const selectedSites = ref<string[]>([...(props.plan?.siteIds ?? [])])
const selectedDatabases = ref<string[]>([...(props.plan?.databaseIds ?? [])])
const busy = ref(false)
const error = ref('')

const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
const pgQuery = useQuery({ queryKey: ['postgres-databases'], queryFn: listPostgresDatabases, retry: false })
const myQuery = useQuery({ queryKey: ['mysql-databases'], queryFn: listMysqlDatabases, retry: false })

const siteOptions = computed(() =>
  (sitesQuery.data.value ?? []).map((site) => ({ value: site.id, label: `${site.displayName} — ${site.primaryDomain}` })),
)
const databaseOptions = computed(() => [
  ...(pgQuery.data.value ?? []).map((db) => ({ value: `postgres:${db.id}`, label: `${db.name} · PostgreSQL` })),
  ...(myQuery.data.value ?? []).map((db) => ({ value: `mysql:${db.id}`, label: `${db.name} · MySQL` })),
])

function toggle(target: 'site' | 'database', value: string) {
  const model = target === 'site' ? selectedSites : selectedDatabases
  model.value = model.value.includes(value) ? model.value.filter((entry) => entry !== value) : [...model.value, value]
}

const preset = computed<string>({
  get: () => presets.find((option) => option.value === cronExpression.value.trim())?.value ?? 'custom',
  set: (value) => {
    if (value !== 'custom') cronExpression.value = value
  },
})

const now = ref(new Date())
const ticker = setInterval(() => {
  now.value = new Date()
}, 30_000)
onBeforeUnmount(() => clearInterval(ticker))

const cronPreview = computed(() => {
  const expression = cronExpression.value.trim()
  if (!expression) return { error: '', sentence: '', runs: [] as Date[] }
  try {
    return { error: '', sentence: describeCron(expression), runs: nextRuns(expression, now.value, 3) }
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : 'The expression is invalid.'
    return { error: message.charAt(0).toUpperCase() + message.slice(1), sentence: '', runs: [] as Date[] }
  }
})

const nextRunsLabel = computed(() => cronPreview.value.runs.map((run) => formatDateTime(run.toISOString())).join(' · '))

const hasTargets = computed(() => selectedSites.value.length + selectedDatabases.value.length > 0)

async function submit() {
  if (cronPreview.value.error) return
  if (!accountId.value) {
    error.value = 'Choose a backup account.'
    return
  }
  if (!hasTargets.value) {
    error.value = 'Select at least one site or database to back up.'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const body: BackupPlanRequest = {
      name: name.value.trim(),
      accountId: accountId.value,
      copiesLimit: Number(copiesLimit.value),
      siteIds: selectedSites.value,
      databaseIds: selectedDatabases.value,
      schedule: normalize(cronExpression.value.trim()),
      enabled: enabled.value,
    }
    if (props.plan) await updateBackupPlan(props.plan.id, body)
    else await createBackupPlan(body)
    emit('saved')
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'The backup plan could not be saved.'
  } finally {
    busy.value = false
  }
}

function close() {
  if (busy.value) return
  emit('close')
}
</script>

<template>
  <AppDialog :open="true" :title="plan ? `Edit ${plan.name}` : 'New backup plan'" size="lg" @close="close">
    <EmptyState
      v-if="!accounts.length"
      icon="archive"
      title="No backup accounts yet"
      description="A plan needs a storage destination. Create a backup account first, then come back to build a plan."
    />
    <form v-else class="space-y-4" @submit.prevent="submit">
      <FormField label="Name">
        <AppInput v-model="name" maxlength="80" autocomplete="off" placeholder="Nightly backup" required />
      </FormField>

      <div class="grid gap-4 sm:grid-cols-2">
        <FormField label="Account" hint="Where copies are stored.">
          <AppSelect v-model="accountId">
            <option v-for="account in accounts" :key="account.id" :value="account.id">{{ account.name }}</option>
          </AppSelect>
        </FormField>
        <FormField label="Backup copies to keep" hint="Older copies beyond this are pruned after each run.">
          <AppInput v-model="copiesLimit" type="number" min="1" max="1000" step="1" required />
        </FormField>
      </div>

      <FormField label="Sites" :hint="siteOptions.length ? undefined : 'No sites available.'">
        <div v-if="siteOptions.length" class="max-h-40 space-y-1 overflow-y-auto rounded-lg border border-outline bg-white/[0.02] p-2">
          <label
            v-for="option in siteOptions"
            :key="option.value"
            class="flex cursor-pointer items-center gap-2.5 rounded px-2 py-1.5 text-[13px] text-ink-secondary hover:bg-white/[0.04]"
          >
            <input
              type="checkbox"
              class="size-4 accent-accent-500"
              :checked="selectedSites.includes(option.value)"
              @change="toggle('site', option.value)"
            />
            {{ option.label }}
          </label>
        </div>
      </FormField>

      <FormField label="Databases" :hint="databaseOptions.length ? undefined : 'No databases available.'">
        <div
          v-if="databaseOptions.length"
          class="max-h-40 space-y-1 overflow-y-auto rounded-lg border border-outline bg-white/[0.02] p-2"
        >
          <label
            v-for="option in databaseOptions"
            :key="option.value"
            class="flex cursor-pointer items-center gap-2.5 rounded px-2 py-1.5 text-[13px] text-ink-secondary hover:bg-white/[0.04]"
          >
            <input
              type="checkbox"
              class="size-4 accent-accent-500"
              :checked="selectedDatabases.includes(option.value)"
              @change="toggle('database', option.value)"
            />
            {{ option.label }}
          </label>
        </div>
      </FormField>

      <FormField label="Schedule preset">
        <AppSelect v-model="preset" aria-label="Schedule preset">
          <option v-for="option in presets" :key="option.value" :value="option.value">{{ option.label }}</option>
          <option value="custom">Custom</option>
        </AppSelect>
      </FormField>

      <FormField
        label="Cron expression"
        hint="Five fields: minute hour day-of-month month day-of-week."
        :error="cronPreview.error"
      >
        <AppInput
          v-model="cronExpression"
          class="font-mono"
          autocomplete="off"
          spellcheck="false"
          placeholder="0 3 * * *"
          :invalid="Boolean(cronPreview.error)"
          required
        />
      </FormField>
      <div v-if="cronPreview.sentence" class="-mt-2 space-y-0.5" aria-live="polite">
        <p class="text-xs text-ink-secondary">{{ cronPreview.sentence }}</p>
        <p v-if="nextRunsLabel" class="flex items-center gap-1.5 text-xs text-ink-muted">
          <AppIcon name="calendar" :size="12" class="shrink-0" />
          Next runs: {{ nextRunsLabel }}
        </p>
      </div>

      <label class="flex items-center gap-2.5 text-[13px] text-ink-secondary">
        <Switch v-model="enabled" />
        Enabled — the schedule runs automatically once installed
      </label>

      <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>
    </form>

    <template v-if="accounts.length" #footer>
      <AppButton :disabled="busy" @click="close">Cancel</AppButton>
      <AppButton variant="primary" :loading="busy" :disabled="Boolean(cronPreview.error)" @click="submit">
        {{ plan ? 'Save' : 'Create plan' }}
      </AppButton>
    </template>
  </AppDialog>
</template>
