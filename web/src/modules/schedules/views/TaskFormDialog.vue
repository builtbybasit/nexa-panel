<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import { AppAlert, AppButton, AppDialog, AppIcon, AppInput, AppSelect, AppTextarea, FormField, Switch } from '@/shared/ui'

import type { Job } from '../../jobs/api'
import { createTask, updateTask, type ScheduledTask } from '../api'
import { describe as describeCron, nextRuns, normalize } from '../cron'

const props = defineProps<{ siteId: string; task?: ScheduledTask | undefined }>()
const emit = defineEmits<{ close: []; queued: [task: ScheduledTask, job: Job] }>()

const presets = [
  { label: 'Every 5 minutes', value: '*/5 * * * *' },
  { label: 'Every 15 minutes', value: '*/15 * * * *' },
  { label: 'Every 30 minutes', value: '*/30 * * * *' },
  { label: 'Hourly', value: '0 * * * *' },
  { label: 'Twice daily (00:00 and 12:00)', value: '0 0,12 * * *' },
  { label: 'Daily at midnight', value: '0 0 * * *' },
  { label: 'Weekly (Sunday at midnight)', value: '0 0 * * 0' },
  { label: 'Monthly (1st at midnight)', value: '0 0 1 * *' },
]

const name = ref(props.task?.name ?? '')
const cronExpression = ref(props.task?.cronExpression ?? '0 0 * * *')
const command = ref(props.task?.command ?? '')
const timeoutSeconds = ref<string | number>(props.task?.timeoutSeconds ?? 300)
const enabled = ref(props.task?.enabled ?? true)
const busy = ref(false)
const error = ref('')

/** Selecting a preset fills the raw expression; hand edits fall back to Custom. */
const preset = computed<string>({
  get: () => presets.find((option) => option.value === cronExpression.value.trim())?.value ?? 'custom',
  set: (value) => {
    if (value !== 'custom') cronExpression.value = value
  },
})

// Ticks every 30s while the dialog is open so the projected runs never drift
// into the past while the form sits idle.
const now = ref(new Date())
const ticker = setInterval(() => {
  now.value = new Date()
}, 30_000)
onBeforeUnmount(() => clearInterval(ticker))

/** Live parse of the expression: a plain sentence and the next three runs, or the parser's message. */
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

const nextRunsLabel = computed(() =>
  cronPreview.value.runs.map((run) => formatDateTime(run.toISOString())).join(' · '),
)

async function submit() {
  if (cronPreview.value.error) return
  busy.value = true
  error.value = ''
  try {
    const input = {
      name: name.value.trim(),
      // Month/day names are normalized to numbers: the server accepts only
      // numeric fields while the preview happily reads JAN or MON-FRI.
      cronExpression: normalize(cronExpression.value.trim()),
      // The command is one logical line; embedded newlines are joined before submit.
      command: command.value.replace(/[\r\n]+/g, ' ').trim(),
      timeoutSeconds: Number(timeoutSeconds.value),
      enabled: enabled.value,
    }
    const result = props.task ? await updateTask(props.siteId, props.task.id, input) : await createTask(props.siteId, input)
    emit('queued', result.task, result.job)
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'The task could not be queued.'
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
  <AppDialog :open="true" :title="task ? `Edit ${task.name}` : 'New task'" @close="close">
    <form class="space-y-4" @submit.prevent="submit">
      <FormField label="Name">
        <AppInput v-model="name" maxlength="64" autocomplete="off" placeholder="Nightly cleanup" required />
      </FormField>

      <FormField label="Schedule preset">
        <AppSelect v-model="preset" aria-label="Schedule preset">
          <option v-for="option in presets" :key="option.value" :value="option.value">{{ option.label }}</option>
          <option value="custom">Custom</option>
        </AppSelect>
      </FormField>

      <FormField
        label="Cron expression"
        hint="Five space-separated fields: minute hour day-of-month month day-of-week."
        :error="cronPreview.error"
      >
        <AppInput
          v-model="cronExpression"
          class="font-mono"
          autocomplete="off"
          spellcheck="false"
          placeholder="0 0 * * *"
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

      <FormField
        label="Command"
        hint="Runs via /bin/sh from the site root as the site's Unix user. Newlines are joined into one line."
      >
        <AppTextarea v-model="command" rows="3" maxlength="2048" spellcheck="false" placeholder="php artisan schedule:run" required />
      </FormField>

      <FormField label="Timeout (seconds)" hint="10 to 86400 seconds; the command is stopped once it runs longer.">
        <AppInput v-model="timeoutSeconds" type="number" min="10" max="86400" step="1" required />
      </FormField>

      <label class="flex items-center gap-2.5 text-[13px] text-ink-secondary">
        <Switch v-model="enabled" />
        Enabled — the cron entry is installed when the plan is applied
      </label>

      <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>

      <div class="flex justify-end gap-2">
        <AppButton :disabled="busy" @click="close">Cancel</AppButton>
        <AppButton variant="primary" type="submit" :loading="busy" :disabled="Boolean(cronPreview.error)">
          {{ task ? 'Save changes' : 'Create task' }}
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
