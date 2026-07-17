<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'

import AppButton from './AppButton.vue'
import AppIcon from './AppIcon.vue'
import AppInput from './AppInput.vue'

const props = withDefaults(
  defineProps<{
    label?: string
    hint?: string
    error?: string
    /** Drives both the input's minlength and the "N symbols" requirement chip. */
    minimumLength?: number
    autocomplete?: string
    placeholder?: string
    required?: boolean
  }>(),
  { label: 'Password', minimumLength: 14, autocomplete: 'new-password' },
)

const model = defineModel<string>({ required: true })

const fieldId = useId()
const visible = ref(false)
const copied = ref(false)
const notice = ref<{ text: string; tone: 'info' | 'error' }>()

const CHARACTER_CLASSES = ['abcdefghijklmnopqrstuvwxyz', 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', '0123456789', '!@#$%^&*-_=+']

function randomBelow(bound: number): number {
  const buffer = new Uint32Array(1)
  crypto.getRandomValues(buffer)
  return (buffer[0] ?? 0) % bound
}

/** Random characters guaranteed to mix all four classes. */
function generatePassword(length: number): string {
  const all = CHARACTER_CLASSES.join('')
  const chars = CHARACTER_CLASSES.map((set) => set.charAt(randomBelow(set.length)))
  while (chars.length < length) chars.push(all.charAt(randomBelow(all.length)))
  for (let i = chars.length - 1; i > 0; i--) {
    const j = randomBelow(i + 1)
    ;[chars[i], chars[j]] = [chars[j] ?? '', chars[i] ?? '']
  }
  return chars.join('')
}

function fillGenerated() {
  // Revealing is the point of generating: nobody can use a password they
  // cannot see, and it is already on screen for them to copy.
  model.value = generatePassword(Math.max(20, props.minimumLength))
  visible.value = true
  notice.value = undefined
}

watch(model, (value) => {
  copied.value = false
  notice.value = undefined
  // Clearing the field is how callers reset the dialog, so re-hide with it
  // rather than leaving the next password revealed on open.
  if (!value) visible.value = false
})

async function copy() {
  try {
    await navigator.clipboard.writeText(model.value)
    copied.value = true
    notice.value = { text: 'Password copied.', tone: 'info' }
  } catch {
    copied.value = false
    notice.value = {
      text: 'Copying was blocked by the browser. Show the password and copy it manually.',
      tone: 'error',
    }
  }
}

const requirements = computed(() => [
  { label: 'Special symbols', met: /[^A-Za-z0-9]/.test(model.value) },
  { label: 'Different cases', met: /[a-z]/.test(model.value) && /[A-Z]/.test(model.value) },
  { label: `${props.minimumLength} symbols`, met: model.value.length >= props.minimumLength },
])

// Withheld until there is something to judge — verdicts on an untouched empty
// field are nagging, not guidance.
const verdict = computed(() => {
  if (!model.value) return undefined
  const met = requirements.value.filter((item) => item.met).length
  if (met <= 1) return { label: 'Weak password', tone: 'border-rose-500/40 bg-rose-500/10 text-rose-300' }
  if (met === 2) return { label: 'Fair password', tone: 'border-amber-500/40 bg-amber-500/10 text-amber-300' }
  return { label: 'Strong password', tone: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300' }
})
</script>

<template>
  <div>
    <div class="mb-1.5 flex items-center justify-between gap-3">
      <label :for="fieldId" class="text-[13px] font-medium text-ink-secondary">{{ label }}</label>
      <button
        type="button"
        class="rounded text-[13px] font-medium text-accent-300 transition-colors hover:text-accent-200"
        @click="fillGenerated"
      >
        Generate
      </button>
    </div>
    <div class="flex gap-2">
      <AppInput
        :id="fieldId"
        v-model="model"
        :type="visible ? 'text' : 'password'"
        class="flex-1"
        :minlength="minimumLength"
        :autocomplete="autocomplete"
        :placeholder="placeholder"
        :required="required"
        :invalid="!!error"
      />
      <AppButton
        :icon="visible ? 'eye-off' : 'eye'"
        :aria-label="visible ? 'Hide password' : 'Show password'"
        :title="visible ? 'Hide password' : 'Show password'"
        @click="visible = !visible"
      />
      <AppButton
        :icon="copied ? 'check' : 'copy'"
        aria-label="Copy password"
        title="Copy password"
        :disabled="!model"
        @click="copy"
      />
    </div>
    <p v-if="error" role="alert" class="mt-1.5 text-xs leading-relaxed text-rose-300">{{ error }}</p>
    <p v-else-if="hint" class="mt-1.5 text-xs leading-relaxed text-ink-muted">{{ hint }}</p>
    <div class="mt-2 flex flex-wrap items-center gap-1.5">
      <span
        v-for="requirement in requirements"
        :key="requirement.label"
        class="inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-[11px] font-medium transition-colors"
        :class="
          requirement.met
            ? 'border-accent-400/40 bg-accent-500/10 text-accent-300'
            : 'border-outline bg-white/[0.03] text-ink-muted'
        "
      >
        <AppIcon v-if="requirement.met" name="check" :size="11" />
        {{ requirement.label }}
        <!-- Colour and the tick carry this visually; spell it out for readers that get neither. -->
        <span class="sr-only">{{ requirement.met ? 'met' : 'not met' }}</span>
      </span>
      <span
        v-if="verdict"
        class="inline-flex items-center gap-1 rounded-full border px-2.5 py-1 text-[11px] font-medium"
        :class="verdict.tone"
      >
        <AppIcon name="lock" :size="11" />
        {{ verdict.label }}
      </span>
    </div>
    <p
      v-if="notice"
      aria-live="polite"
      class="mt-1.5 text-xs leading-relaxed"
      :class="notice.tone === 'error' ? 'text-rose-300' : 'text-ink-muted'"
    >
      {{ notice.text }}
    </p>
  </div>
</template>
