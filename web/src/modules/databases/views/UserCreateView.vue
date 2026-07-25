<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

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
  PasswordField,
} from '@/shared/ui'
import {
  Combobox,
  ComboboxAnchor,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxItemIndicator,
  ComboboxList,
  ComboboxTrigger,
} from '@/shared/ui/combobox'

import { useIdentityStore } from '@/modules/identity/store'

import { createUser } from '../api'
import { useDatabasesData } from '../composables/useDatabasesData'
import { ENGINES, serverLabel } from '../lib/engines'

const NAME_RULE = /^[a-z][a-z0-9_]+$/
const NAME_HINT = 'Lowercase letters, numbers, and underscores; starts with a letter.'
const NAME_ERROR = 'Use lowercase letters, numbers, and underscores, starting with a letter (at least 2 characters).'

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))

const data = useDatabasesData()
const { activeServers } = data

const runner = useJobRunner()

// A link can preselect the server (`?server=<id>`), e.g. from the create page.
const serverId = ref(typeof route.query.server === 'string' ? route.query.server : '')
const name = ref('')
const host = ref('localhost')
const password = ref('')
const attempted = ref(false)

const selectedServer = computed(() => data.server(serverId.value))
const hostVisible = computed(() => !!selectedServer.value && ENGINES[selectedServer.value.engine].userHostScopes)

const serverError = computed(() => (attempted.value && !serverId.value ? 'Select a server.' : ''))
const nameError = computed(() => (attempted.value && !NAME_RULE.test(name.value) ? NAME_ERROR : ''))
const hostError = computed(() =>
  attempted.value && hostVisible.value && !host.value.trim() ? 'Enter a host, or % to allow any host.' : '',
)
const passwordError = computed(() =>
  attempted.value && password.value.length < 8 ? 'Use at least 8 characters — or generate one.' : '',
)

async function submit() {
  if (!canWrite.value || runner.busy.value) return
  attempted.value = true
  if (serverError.value || nameError.value || hostError.value || passwordError.value) return
  await runner.run(
    async () =>
      (
        await createUser({
          serverId: serverId.value,
          name: name.value,
          password: password.value,
          ...(hostVisible.value ? { host: host.value } : {}),
        })
      ).job.id,
    {
      onSettled: data.refreshAll,
      successToast: `User ${name.value} is ready`,
      failureMessage: 'Creating the user failed',
    },
  )
  if (!runner.error.value) await router.push('/databases')
}
</script>

<template>
  <section class="mx-auto max-w-2xl space-y-6">
    <PageHeader
      :breadcrumbs="[{ label: 'Databases', to: '/databases' }, { label: 'Add user' }]"
      title="Add database user"
      description="A login identity that can own databases and be granted access to others."
    />

    <AppAlert v-if="!canWrite" tone="info">
      Your account has read-only access to databases.
      <RouterLink to="/databases" class="ml-1 underline underline-offset-2">Back to databases</RouterLink>
    </AppAlert>

    <AppCard v-else>
      <form class="space-y-5" novalidate @submit.prevent="submit">
        <FormField label="Server" :error="serverError">
          <Combobox v-model="serverId" :disabled="runner.busy.value">
            <ComboboxAnchor as-child>
              <ComboboxTrigger
                :invalid="!!serverError"
                placeholder="Select server"
                :label="((id) => {
                  const item = activeServers.find((server) => server.id === id)
                  return item ? serverLabel(item) : ''
                })(serverId)"
              />
            </ComboboxAnchor>
            <ComboboxList>
              <ComboboxInput placeholder="Search servers…" />
              <ComboboxEmpty>No active servers — install or provision one first</ComboboxEmpty>
              <ComboboxGroup>
                <ComboboxItem
                  v-for="item in activeServers"
                  :key="item.id"
                  :value="item.id"
                  :text-value="serverLabel(item)"
                >
                  {{ serverLabel(item) }}<ComboboxItemIndicator />
                </ComboboxItem>
              </ComboboxGroup>
            </ComboboxList>
          </Combobox>
        </FormField>

        <FormField label="Login" :hint="NAME_HINT" :error="nameError">
          <AppInput
            v-model="name"
            autocomplete="off"
            spellcheck="false"
            :invalid="!!nameError"
            :disabled="runner.busy.value"
          />
        </FormField>

        <FormField
          v-if="hostVisible"
          label="Host"
          hint="Where this user may connect from: localhost, %, or a specific address."
          :error="hostError"
        >
          <AppInput
            v-model="host"
            autocomplete="off"
            spellcheck="false"
            :invalid="!!hostError"
            :disabled="runner.busy.value"
          />
        </FormField>

        <PasswordField
          v-model="password"
          label="Password"
          :minimum-length="8"
          :maximum-length="128"
          :error="passwordError"
          hint="Copy it now — the panel stores it encrypted and never shows it again."
        />

        <JobFailureNotice v-if="runner.error.value" v-bind="runner.failureProps.value" />
        <JobProgress v-if="runner.progress.value" :event="runner.progress.value" v-bind="runner.progressProps.value" />

        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <RouterLink to="/databases"><AppButton :disabled="runner.busy.value">Cancel</AppButton></RouterLink>
          <AppButton variant="primary" type="submit" :loading="runner.busy.value">Add user</AppButton>
        </div>
      </form>
    </AppCard>
  </section>
</template>
