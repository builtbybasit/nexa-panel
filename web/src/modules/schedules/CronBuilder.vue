<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import { AppIcon, AppInput } from '@/shared/ui'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'

import { describe as describeCron, nextRuns } from './cron'

// FastPanel-style cron builder: one row per field (minute, hour, day-of-month,
// month, day-of-week), each a preset dropdown plus a value box that unlocks on
// "Custom". The rows compose the same five-field expression the rest of the app
// already parses, so v-model stays a plain cron string and the server remains
// the validation authority.

const model = defineModel<string>({ default: '0 0 * * *' })
const emit = defineEmits<{ 'update:valid': [valid: boolean] }>()

type FieldKey = 'minute' | 'hour' | 'dayOfMonth' | 'month' | 'dayOfWeek'

interface FieldDef {
  key: FieldKey
  label: string
  hint: string
  placeholder: string
  presets: { label: string; value: string }[]
}

const CUSTOM = 'custom'

const FIELDS: FieldDef[] = [
  {
    key: 'minute',
    label: 'Minute',
    hint: '0–59',
    placeholder: '0',
    presets: [
      { label: 'Every minute', value: '*' },
      { label: 'Every 5 minutes', value: '*/5' },
      { label: 'Every 10 minutes', value: '*/10' },
      { label: 'Every 15 minutes', value: '*/15' },
      { label: 'Every 30 minutes', value: '*/30' },
    ],
  },
  {
    key: 'hour',
    label: 'Hour',
    hint: '0–23',
    placeholder: '0',
    presets: [
      { label: 'Every hour', value: '*' },
      { label: 'Every 2 hours', value: '*/2' },
      { label: 'Every 3 hours', value: '*/3' },
      { label: 'Every 6 hours', value: '*/6' },
      { label: 'Every 12 hours', value: '*/12' },
    ],
  },
  {
    key: 'dayOfMonth',
    label: 'Day of month',
    hint: '1–31',
    placeholder: '1',
    presets: [
      { label: 'Every day', value: '*' },
      { label: 'On the 1st', value: '1' },
      { label: 'On the 15th', value: '15' },
    ],
  },
  {
    key: 'month',
    label: 'Month',
    hint: '1–12',
    placeholder: '1',
    presets: [
      { label: 'Every month', value: '*' },
      { label: 'Quarterly', value: '*/3' },
    ],
  },
  {
    key: 'dayOfWeek',
    label: 'Day of week',
    hint: '0–6 (Sun–Sat)',
    placeholder: '1',
    presets: [
      { label: 'Every day', value: '*' },
      { label: 'Weekdays (Mon–Fri)', value: '1-5' },
      { label: 'Weekends (Sat–Sun)', value: '0,6' },
    ],
  },
]

// Per-field UI state: which preset is chosen (or CUSTOM), and the custom box text.
const modes = reactive<Record<FieldKey, string>>({
  minute: '*',
  hour: '*',
  dayOfMonth: '*',
  month: '*',
  dayOfWeek: '*',
})
const customValues = reactive<Record<FieldKey, string>>({
  minute: '',
  hour: '',
  dayOfMonth: '',
  month: '',
  dayOfWeek: '',
})

/** The effective cron token for a field, honouring preset vs custom. */
function tokenFor(key: FieldKey): string {
  return modes[key] === CUSTOM ? customValues[key].trim() : modes[key]
}

/** What the value box shows: the custom text when editable, else the preset token. */
function displayValue(key: FieldKey): string {
  return modes[key] === CUSTOM ? customValues[key] : modes[key]
}

/** Split an incoming expression into the five field boxes. */
function hydrate(expression: string): void {
  const tokens = (expression ?? '').trim().split(/\s+/)
  FIELDS.forEach((field, index) => {
    const token = tokens[index] ?? '*'
    const preset = field.presets.find((option) => option.value === token)
    if (preset) {
      modes[field.key] = token
      customValues[field.key] = token
    } else {
      modes[field.key] = CUSTOM
      customValues[field.key] = token
    }
  })
}

hydrate(model.value)

function onModeChange(key: FieldKey, value: string): void {
  if (value === CUSTOM) {
    // Seed the custom box from the current token so switching never blanks it.
    const current = modes[key] === CUSTOM ? customValues[key] : modes[key]
    customValues[key] = current
  }
  modes[key] = value
}

const composed = computed(() => FIELDS.map((field) => tokenFor(field.key)).join(' '))

const now = ref(new Date())
const ticker = setInterval(() => {
  now.value = new Date()
}, 30_000)
onBeforeUnmount(() => clearInterval(ticker))

const preview = computed(() => {
  const empty = FIELDS.find((field) => modes[field.key] === CUSTOM && !customValues[field.key].trim())
  if (empty) return { error: `Enter a value for ${empty.label.toLowerCase()}.`, sentence: '', runs: [] as Date[] }
  try {
    return { error: '', sentence: describeCron(composed.value), runs: nextRuns(composed.value, now.value, 3) }
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : 'The expression is invalid.'
    return { error: message.charAt(0).toUpperCase() + message.slice(1), sentence: '', runs: [] as Date[] }
  }
})

const nextRunsLabel = computed(() => preview.value.runs.map((run) => formatDateTime(run.toISOString())).join(' · '))

// Push composed expression + validity outward; re-hydrate only when the parent
// sets a value that differs from what the rows currently spell out.
watch(
  [composed, () => preview.value.error],
  () => {
    if (composed.value !== model.value) model.value = composed.value
    emit('update:valid', !preview.value.error)
  },
  { immediate: true },
)

watch(
  () => model.value,
  (value) => {
    if ((value ?? '').trim() !== composed.value.trim()) hydrate(value)
  },
)
</script>

<template>
  <div class="space-y-3">
    <div class="grid gap-x-3 gap-y-3 sm:grid-cols-2">
      <div v-for="field in FIELDS" :key="field.key">
        <span class="mb-1.5 block text-[13px] font-medium text-ink-secondary">{{ field.label }}</span>
        <div class="grid grid-cols-[5rem_1fr] gap-2">
          <AppInput
            :model-value="displayValue(field.key)"
            :disabled="modes[field.key] !== CUSTOM"
            :placeholder="field.placeholder"
            :invalid="modes[field.key] === CUSTOM && !customValues[field.key].trim()"
            class="font-mono"
            autocomplete="off"
            spellcheck="false"
            :aria-label="`${field.label} value`"
            @update:model-value="customValues[field.key] = String($event ?? '')"
          />
          <Select
            :model-value="modes[field.key]"
            @update:model-value="onModeChange(field.key, String($event))"
          >
            <SelectTrigger :aria-label="`${field.label} preset`" />
            <SelectContent>
              <SelectItem v-for="option in field.presets" :key="option.value" :value="option.value">{{ option.label }}</SelectItem>
              <SelectItem :value="CUSTOM">Custom…</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <span class="mt-1 block text-[11px] text-ink-muted">{{ field.hint }}</span>
      </div>
    </div>

    <div
      class="flex flex-col gap-1 rounded-lg border border-outline bg-white/[0.02] px-3 py-2.5"
      aria-live="polite"
    >
      <div v-if="preview.error" class="flex items-center gap-1.5 text-xs text-rose-300">
        <AppIcon name="triangle-alert" :size="13" class="shrink-0" />
        {{ preview.error }}
      </div>
      <template v-else>
        <div class="flex items-center gap-2 text-[13px] text-ink-secondary">
          <code class="rounded bg-white/[0.04] px-1.5 py-0.5 font-mono text-xs text-ink">{{ composed }}</code>
          <span>{{ preview.sentence }}</span>
        </div>
        <p v-if="nextRunsLabel" class="flex items-center gap-1.5 text-xs text-ink-muted">
          <AppIcon name="calendar" :size="12" class="shrink-0" />
          Next runs: {{ nextRunsLabel }}
        </p>
      </template>
    </div>
  </div>
</template>
