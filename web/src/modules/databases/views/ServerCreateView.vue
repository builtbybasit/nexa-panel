<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'

import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppInput,
  FormField,
  JobFailureNotice,
  JobProgress,
  PageHeader,
} from '@/shared/ui'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/shared/ui/select'

import { useIdentityStore } from '@/modules/identity/store'

import { createServer } from '../api'
import { failureProps, progressProps } from '../composables/runnerProps'
import { useDatabasesData } from '../composables/useDatabasesData'

const NAME_RULE = /^[a-z][a-z0-9_]+$/
const NAME_HINT = 'Lowercase letters, numbers, and underscores; starts with a letter.'
const NAME_ERROR = 'Use lowercase letters, numbers, and underscores, starting with a letter (at least 2 characters).'

const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))

const data = useDatabasesData()
const runner = useJobRunner()

const version = ref<'16' | '17' | '18'>('18')
const cluster = ref('nexa_main')
const port = ref<number | undefined>()
const attempted = ref(false)

const clusterError = computed(() => (attempted.value && !NAME_RULE.test(cluster.value) ? NAME_ERROR : ''))
const portError = computed(() => {
  if (!attempted.value || typeof port.value !== 'number') return ''
  return port.value >= 1024 && port.value <= 65535 ? '' : 'Use a port between 1024 and 65535, or leave it empty.'
})

async function submit() {
  if (!canWrite.value || runner.busy.value) return
  attempted.value = true
  if (clusterError.value || portError.value) return
  await runner.run(
    async () =>
      (
        await createServer({
          engine: 'postgresql',
          version: version.value,
          cluster: cluster.value,
          ...(typeof port.value === 'number' ? { port: port.value } : {}),
        })
      ).job.id,
    {
      onSettled: data.refreshAll,
      successToast: `PostgreSQL ${version.value} · ${cluster.value} is ready`,
      failureMessage: 'Provisioning the instance failed',
    },
  )
  if (!runner.error.value) await router.push('/databases')
}
</script>

<template>
  <section class="mx-auto max-w-2xl space-y-6">
    <PageHeader
      :breadcrumbs="[{ label: 'Databases', to: '/databases' }, { label: 'Provision instance' }]"
      title="Provision PostgreSQL instance"
      description="Each instance gets its own port, data path, and systemd unit. The MySQL-family server is discovered from the host instead."
    />

    <AppAlert v-if="!canWrite" tone="info">
      Your account has read-only access to databases.
      <RouterLink to="/databases" class="ml-1 underline underline-offset-2">Back to databases</RouterLink>
    </AppAlert>

    <AppCard v-else>
      <form class="space-y-5" novalidate @submit.prevent="submit">
        <FormField label="Version">
          <Select v-model="version" :disabled="runner.busy.value">
            <SelectTrigger placeholder="Select version" />
            <SelectContent>
              <SelectItem value="16">PostgreSQL 16</SelectItem>
              <SelectItem value="17">PostgreSQL 17</SelectItem>
              <SelectItem value="18">PostgreSQL 18</SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="Cluster name" :hint="NAME_HINT" :error="clusterError">
          <AppInput
            v-model="cluster"
            autocomplete="off"
            spellcheck="false"
            :invalid="!!clusterError"
            :disabled="runner.busy.value"
          />
        </FormField>
        <FormField label="Port" hint="Leave empty to assign a free port automatically." :error="portError">
          <AppInput
            v-model.number="port"
            type="number"
            min="1024"
            max="65535"
            :invalid="!!portError"
            :disabled="runner.busy.value"
          />
        </FormField>
        <JobFailureNotice v-if="runner.error.value" v-bind="failureProps(runner)" />
        <JobProgress v-if="runner.progress.value" :event="runner.progress.value" v-bind="progressProps(runner)" />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <RouterLink to="/databases"><AppButton :disabled="runner.busy.value">Cancel</AppButton></RouterLink>
          <AppButton variant="primary" type="submit" :loading="runner.busy.value">Provision instance</AppButton>
        </div>
      </form>
    </AppCard>
  </section>
</template>
