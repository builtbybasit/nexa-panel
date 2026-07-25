<script setup lang="ts">
import { computed, ref, toRef, watch } from 'vue'

import { AppAlert, AppButton, AppDialog, AppInput, Checkbox, FormField } from '@/shared/ui'

import { changeMode, type FileEntry } from '../api'
import { useDialogForm } from '../composables/useDialogForm'
import { joinPath, symbolicToOctal } from '../lib'

const props = defineProps<{ open: boolean; siteId: string; directory: string; entry: FileEntry | undefined }>()
const emit = defineEmits<{ close: []; done: [] }>()

const mode = ref('644')
const { busy, error, submit } = useDialogForm(
  toRef(props, 'open'),
  () => {
    emit('done')
    emit('close')
  },
)

watch(
  () => [props.open, props.entry?.mode] as const,
  ([open]) => {
    if (open) mode.value = props.entry ? symbolicToOctal(props.entry.mode) || '644' : '644'
  },
  { immediate: true },
)

const valid = computed(() => /^[0-7]{3}$/.test(mode.value))

const rows: { label: string; index: number }[] = [
  { label: 'Owner', index: 0 },
  { label: 'Group', index: 1 },
  { label: 'Public', index: 2 },
]
const bits: { label: string; bit: number }[] = [
  { label: 'Read', bit: 4 },
  { label: 'Write', bit: 2 },
  { label: 'Execute', bit: 1 },
]

function checked(index: number, bit: number): boolean {
  if (!valid.value) return false
  return (parseInt(mode.value[index] ?? '0', 8) & bit) !== 0
}

function setBit(index: number, bit: number, on: boolean | 'indeterminate') {
  if (!valid.value) return
  const digits = mode.value.split('').map((digit) => parseInt(digit, 8))
  const current = digits[index] ?? 0
  digits[index] = on === true ? current | bit : current & ~bit
  mode.value = digits.map((digit) => digit.toString(8)).join('')
}

function onSubmit() {
  const target = props.entry
  if (!target || !valid.value) return
  void submit(async () => {
    await changeMode(props.siteId, { path: joinPath(props.directory, target.name), mode: mode.value })
  })
}
</script>

<template>
  <AppDialog :open="open" :title="`Permissions — ${entry?.name ?? ''}`" @close="emit('close')">
    <form class="space-y-4" @submit.prevent="onSubmit">
      <div class="overflow-hidden rounded-xl border border-outline">
        <table class="w-full text-left">
          <thead>
            <tr class="border-b border-outline bg-white/[0.02]">
              <th class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase"></th>
              <th v-for="bit in bits" :key="bit.bit" class="px-3 py-2 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">
                {{ bit.label }}
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-outline">
            <tr v-for="row in rows" :key="row.index">
              <td class="px-3 py-2.5 text-[13px] font-medium text-ink">{{ row.label }}</td>
              <td v-for="bit in bits" :key="bit.bit" class="px-3 py-2.5">
                <Checkbox
                  :model-value="checked(row.index, bit.bit)"
                  :aria-label="`${row.label} ${bit.label}`"
                  :disabled="!valid"
                  @update:model-value="(value) => setBit(row.index, bit.bit, value)"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <FormField label="Octal value" hint="Three octal digits, such as 755. Setuid, setgid, and sticky bits are not allowed.">
        <AppInput v-model="mode" class="w-24 font-mono" autocomplete="off" pattern="[0-7]{3}" required />
      </FormField>
      <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>
      <div class="flex justify-end gap-2">
        <AppButton :disabled="busy" @click="emit('close')">Cancel</AppButton>
        <AppButton variant="primary" type="submit" :loading="busy" :disabled="!valid">Apply permissions</AppButton>
      </div>
    </form>
  </AppDialog>
</template>
