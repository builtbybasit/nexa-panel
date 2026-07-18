<script setup lang="ts">
import { ref } from 'vue'

import { AppAlert, AppButton, AppDialog, AppInput, AppTextarea, FormField, Switch } from '@/shared/ui'

import type { Job } from '../../jobs/api'
import { createTask, updateTask, type ScheduledTask } from '../api'
import CronBuilder from '../CronBuilder.vue'
import { normalize } from '../cron'

const props = defineProps<{ siteId: string; task?: ScheduledTask | undefined }>()
const emit = defineEmits<{ close: []; queued: [task: ScheduledTask, job: Job] }>()

const name = ref(props.task?.name ?? '')
const cronExpression = ref(props.task?.cronExpression ?? '0 0 * * *')
const cronValid = ref(true)
const command = ref(props.task?.command ?? '')
const timeoutSeconds = ref<string | number>(props.task?.timeoutSeconds ?? 300)
const enabled = ref(props.task?.enabled ?? true)
const busy = ref(false)
const error = ref('')

async function submit() {
  if (!cronValid.value) return
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

      <FormField label="Schedule">
        <CronBuilder v-model="cronExpression" @update:valid="cronValid = $event" />
      </FormField>

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
        <AppButton variant="primary" type="submit" :loading="busy" :disabled="!cronValid">
          {{ task ? 'Save changes' : 'Create task' }}
        </AppButton>
      </div>
    </form>
  </AppDialog>
</template>
