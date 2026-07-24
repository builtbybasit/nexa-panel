<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'

import { generatePassword } from '@/shared/lib/password'

import AppButton from './AppButton.vue'
import AppIcon from './AppIcon.vue'
import AppInput from './AppInput.vue'

const props = withDefaults(
  defineProps<{
    label?: string
    hint?: string
    error?: string
    /** Mirrors the server's set-time password policy. */
    minimumLength?: number
    maximumLength?: number
    requiredClasses?: number
    classExemptLength?: number
    autocomplete?: string
    placeholder?: string
    required?: boolean
    /** Renders a FastPanel-style "Confirm the password" input bound to v-model:confirmation. */
    withConfirmation?: boolean
    /** Parent-driven error under the confirmation input (e.g. empty on submit). */
    confirmError?: string
  }>(),
  {
    label: 'Password',
    minimumLength: 14,
    maximumLength: 1024,
    requiredClasses: 3,
    classExemptLength: 20,
    autocomplete: 'new-password',
  },
)

const model = defineModel<string>({ required: true })
const confirmation = defineModel<string>('confirmation', { default: '' })

const fieldId = useId()
const confirmId = useId()
const visible = ref(false)
const copied = ref(false)
const notice = ref<{ text: string; tone: 'info' | 'error' }>()

function fillGenerated() {
  // Revealing is the point of generating: nobody can use a password they
  // cannot see, and it is already on screen for them to copy. The confirmation
  // is filled too — retyping a visible generated password would be busywork.
  model.value = generatePassword(Math.max(20, props.minimumLength))
  if (props.withConfirmation) confirmation.value = model.value
  visible.value = true
  notice.value = undefined
}

// Mismatch is announced as soon as there is a confirmation to compare, not
// only on submit — that is the whole point of confirming.
const mismatch = computed(
  () => props.withConfirmation && confirmation.value !== '' && confirmation.value !== model.value,
)
const confirmMessage = computed(() => props.confirmError || (mismatch.value ? 'The passwords do not match.' : ''))

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

const characterClassCount = computed(() =>
  [/[a-z]/, /[A-Z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((pattern) => pattern.test(model.value)).length,
)
const formatSatisfied = computed(
  () => characterClassCount.value >= props.requiredClasses || model.value.length >= props.classExemptLength,
)
const requirements = computed(() => [
  {
    label: `${props.minimumLength}–${props.maximumLength} characters`,
    met: model.value.length >= props.minimumLength && model.value.length <= props.maximumLength,
  },
  {
    label: `${props.requiredClasses} character types or ${props.classExemptLength}+ character passphrase`,
    met: formatSatisfied.value,
  },
])

// Withheld until there is something to judge — verdicts on an untouched empty
// field are nagging, not guidance.
const verdict = computed(() => {
  if (!model.value) return undefined
  const met = requirements.value.filter((item) => item.met).length
  if (met < requirements.value.length) {
    return { label: 'Policy not met', tone: 'border-rose-500/40 bg-rose-500/10 text-rose-300' }
  }
  return { label: 'Format accepted', tone: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300' }
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
        :maxlength="maximumLength"
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
    <div v-if="withConfirmation" class="mt-3">
      <label :for="confirmId" class="mb-1.5 block text-[13px] font-medium text-ink-secondary">Confirm the password</label>
      <AppInput
        :id="confirmId"
        v-model="confirmation"
        :type="visible ? 'text' : 'password'"
        :maxlength="maximumLength"
        :autocomplete="autocomplete"
        :required="required"
        :invalid="!!confirmMessage"
      />
      <p v-if="confirmMessage" role="alert" class="mt-1.5 text-xs leading-relaxed text-rose-300">{{ confirmMessage }}</p>
    </div>
  </div>
</template>
