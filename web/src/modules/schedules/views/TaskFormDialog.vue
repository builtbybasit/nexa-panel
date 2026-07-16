<script setup lang="ts">
import { computed, ref } from 'vue'

import { AppAlert, AppButton, AppCard, AppInput, AppSelect, AppTextarea, FormField } from '@/shared/ui'

import type { Job } from '../../jobs/api'
import { createTask, updateTask, type ScheduledTask } from '../api'

const props = defineProps<{ siteId: string; task?: ScheduledTask | undefined }>()
const emit = defineEmits<{ close: []; queued: [task: ScheduledTask, job: Job] }>()

const presets = [
  { label: 'Every minute', value: '* * * * *' },
  { label: 'Hourly', value: '0 * * * *' },
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

// Client-side shape hint only — the server validates the expression authoritatively.
const cronItem = String.raw`(\*|\d+-\d+)(\/\d+)?|\d+`
const cronFieldPattern = new RegExp(`^(${cronItem})(,(${cronItem}))*$`)
const cronFieldNames = ['minute', 'hour', 'day-of-month', 'month', 'day-of-week']

const cronWarning = computed(() => {
  const expression = cronExpression.value.trim()
  if (!expression) return ''
  const fields = expression.split(/\s+/)
  if (fields.length !== 5) return `Expected 5 fields but found ${fields.length}.`
  const invalid = fields.findIndex((field) => !cronFieldPattern.test(field))
  if (invalid >= 0) {
    return `The ${cronFieldNames[invalid]} field does not look valid (use *, N, N-M, comma lists, and /step on * or ranges).`
  }
  return ''
})

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const input = {
      name: name.value.trim(),
      cronExpression: cronExpression.value.trim(),
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
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true">
    <div class="absolute inset-0 bg-canvas/70 backdrop-blur-sm" aria-hidden="true" @click="close" />
    <div class="relative w-full max-w-lg">
      <AppCard :eyebrow="task ? 'Edit task' : 'New task'" :title="task ? task.name : 'Schedule a command'">
        <form class="space-y-4" @submit.prevent="submit">
          <FormField label="Name">
            <AppInput v-model="name" maxlength="64" autocomplete="off" placeholder="Nightly cleanup" required />
          </FormField>

          <FormField label="Schedule preset">
            <AppSelect v-model="preset" aria-label="Cron preset">
              <option v-for="option in presets" :key="option.value" :value="option.value">{{ option.label }}</option>
              <option value="custom">Custom</option>
            </AppSelect>
          </FormField>

          <FormField
            label="Cron expression"
            hint="Five space-separated fields: minute hour day-of-month month day-of-week. The server validates the expression while planning."
          >
            <AppInput v-model="cronExpression" class="font-mono" autocomplete="off" spellcheck="false" placeholder="0 0 * * *" required />
          </FormField>
          <p v-if="cronWarning" class="-mt-2 text-xs text-amber-300">{{ cronWarning }}</p>

          <FormField
            label="Command"
            hint="Runs via /bin/sh from the site root as the site's Unix user. Newlines are joined into one logical line."
          >
            <AppTextarea v-model="command" rows="3" maxlength="2048" spellcheck="false" placeholder="php artisan schedule:run" required />
          </FormField>

          <FormField label="Timeout (seconds)" hint="10 to 86400 seconds; the command is terminated once it runs longer.">
            <AppInput v-model="timeoutSeconds" type="number" min="10" max="86400" step="1" required />
          </FormField>

          <label class="flex cursor-pointer items-center gap-2.5 text-[13px] text-ink-secondary">
            <input v-model="enabled" type="checkbox" class="accent-accent-500" />
            Enabled — the cron entry is installed when the plan is applied
          </label>

          <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>

          <div class="flex justify-end gap-2">
            <AppButton :disabled="busy" @click="close">Cancel</AppButton>
            <AppButton variant="primary" type="submit" :loading="busy">{{ task ? 'Save and replan' : 'Create and plan' }}</AppButton>
          </div>
        </form>
      </AppCard>
    </div>
  </div>
</template>
