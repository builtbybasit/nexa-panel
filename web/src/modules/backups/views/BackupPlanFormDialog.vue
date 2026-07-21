<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { AppAlert, AppButton, AppDialog, AppInput, EmptyState, FormField, Switch } from '@/shared/ui'
import { Combobox, ComboboxAnchor, ComboboxEmpty, ComboboxGroup, ComboboxInput, ComboboxItem, ComboboxItemIndicator, ComboboxList, ComboboxTrigger } from '@/shared/ui/combobox'

import { listDatabases as listPostgresDatabases } from '../../databases/api'
import { listDatabases as listMysqlDatabases } from '../../mysql/api'
import CronBuilder from '../../schedules/CronBuilder.vue'
import { normalize } from '../../schedules/cron'
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

const name = ref(props.plan?.name ?? '')
const accountId = ref(props.plan?.accountId ?? props.accounts[0]?.id ?? '')
const copiesLimit = ref<string | number>(props.plan?.copiesLimit ?? 10)
const cronExpression = ref(props.plan?.schedule ?? '0 3 * * *')
const cronValid = ref(true)
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

const hasTargets = computed(() => selectedSites.value.length + selectedDatabases.value.length > 0)

async function submit() {
  if (!cronValid.value) return
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
          <Combobox v-model="accountId">
            <ComboboxAnchor as-child>
              <ComboboxTrigger
                :label="((id) => accounts.find((account) => account.id === id)?.name ?? '')(accountId)"
              />
            </ComboboxAnchor>
            <ComboboxList>
              <ComboboxInput placeholder="Search accounts…" />
              <ComboboxEmpty>No matches.</ComboboxEmpty>
              <ComboboxGroup>
                <ComboboxItem v-for="account in accounts" :key="account.id" :value="account.id" :text-value="account.name">
                  {{ account.name }}<ComboboxItemIndicator />
                </ComboboxItem>
              </ComboboxGroup>
            </ComboboxList>
          </Combobox>
        </FormField>
        <FormField label="Retention target" hint="Recorded now for policy; automatic pruning is not enabled until copy verification is available.">
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

      <FormField label="Schedule">
        <CronBuilder v-model="cronExpression" @update:valid="cronValid = $event" />
      </FormField>

      <label class="flex items-center gap-2.5 text-[13px] text-ink-secondary">
        <Switch v-model="enabled" />
        Enabled — the schedule runs automatically once installed
      </label>

      <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>
    </form>

    <template v-if="accounts.length" #footer>
      <AppButton :disabled="busy" @click="close">Cancel</AppButton>
      <AppButton variant="primary" :loading="busy" :disabled="!cronValid" @click="submit">
        {{ plan ? 'Save' : 'Create plan' }}
      </AppButton>
    </template>
  </AppDialog>
</template>
