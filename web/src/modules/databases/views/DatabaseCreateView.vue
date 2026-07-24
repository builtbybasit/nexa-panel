<script setup lang="ts">
import { computed, ref, watch } from 'vue'
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

import { createDatabase, createUser } from '../api'
import { failureProps, progressProps } from '../composables/runnerProps'
import { useDatabasesData } from '../composables/useDatabasesData'
import { ENGINES, serverLabel, userLabel } from '../lib/engines'

const NAME_RULE = /^[a-z][a-z0-9_]+$/
const NAME_HINT = 'Lowercase letters, numbers, and underscores; starts with a letter.'
const NAME_ERROR = 'Use lowercase letters, numbers, and underscores, starting with a letter (at least 2 characters).'

/** Sentinel option in the owner picker, FastPanel-style. */
const CREATE_NEW_USER = '__new__'

const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))

const data = useDatabasesData()
const { activeServers, activeUsers } = data

const userRunner = useJobRunner()
const databaseRunner = useJobRunner()

// --- Form state ---

const name = ref('')
const serverId = ref('')
const owner = ref(CREATE_NEW_USER)
const newUserName = ref('')
const newUserHost = ref('localhost')
const password = ref('')
const attempted = ref(false)

const selectedServer = computed(() => data.server(serverId.value))
const creatingUser = computed(() => owner.value === CREATE_NEW_USER)
const hostVisible = computed(
  () => creatingUser.value && !!selectedServer.value && ENGINES[selectedServer.value.engine].userHostScopes,
)
/** Users that can own the new database: active users on the chosen server. */
const ownerOptions = computed(() => activeUsers.value.filter((user) => user.serverId === serverId.value))

const nameError = computed(() => (attempted.value && !NAME_RULE.test(name.value) ? NAME_ERROR : ''))
const serverError = computed(() => (attempted.value && !serverId.value ? 'Select a server.' : ''))
const newUserError = computed(() =>
  attempted.value && creatingUser.value && !NAME_RULE.test(newUserName.value) ? NAME_ERROR : '',
)
const hostError = computed(() =>
  attempted.value && hostVisible.value && !newUserHost.value.trim() ? 'Enter a host, or % to allow any host.' : '',
)
const passwordError = computed(() =>
  attempted.value && creatingUser.value && password.value.length < 8
    ? 'Use at least 8 characters — or generate one.'
    : '',
)

// The chosen server decides which users are valid and whether hosts exist, so
// changing it resets an owner that no longer fits.
watch(serverId, () => {
  if (owner.value !== CREATE_NEW_USER && !ownerOptions.value.some((user) => user.id === owner.value)) {
    owner.value = CREATE_NEW_USER
  }
})

function ownerTriggerLabel(id: string): string {
  if (id === CREATE_NEW_USER) return 'Create a new user'
  const user = ownerOptions.value.find((item) => item.id === id)
  return user ? userLabel(user) : ''
}

// --- One-click submit ---
// A new owner and the database are two engine changes, but one decision: the
// jobs run back-to-back and the page simply narrates them.

const submitted = ref(false)
const stage = ref<'user' | 'database'>()

async function submit() {
  if (!canWrite.value || submitted.value) return
  attempted.value = true
  if (nameError.value || serverError.value || newUserError.value || hostError.value || passwordError.value) return
  submitted.value = true
  let ownerUserId = creatingUser.value ? '' : owner.value
  if (creatingUser.value) {
    stage.value = 'user'
    const server = selectedServer.value
    await userRunner.run(
      async () => {
        const result = await createUser({
          serverId: serverId.value,
          name: newUserName.value,
          password: password.value,
          ...(server && ENGINES[server.engine].userHostScopes ? { host: newUserHost.value } : {}),
        })
        ownerUserId = result.user.id
        return result.job.id
      },
      { onSettled: data.refreshAll, failureMessage: 'Creating the user failed' },
    )
    if (userRunner.error.value || !ownerUserId) {
      submitted.value = false
      return
    }
  }
  stage.value = 'database'
  let createdId = ''
  await databaseRunner.run(
    async () => {
      const result = await createDatabase({ serverId: serverId.value, name: name.value, ownerUserId })
      createdId = result.database.id
      return result.job.id
    },
    {
      onSettled: data.refreshAll,
      successToast: `Database ${name.value} is ready`,
      failureMessage: 'Creating the database failed',
    },
  )
  if (databaseRunner.error.value) {
    // The owner may already exist now; flip the picker so a retry does not
    // try to create the same user twice.
    if (creatingUser.value && ownerUserId) owner.value = ownerUserId
    submitted.value = false
    return
  }
  await router.push(`/databases/${encodeURIComponent(createdId)}`)
}
</script>

<template>
  <section class="mx-auto max-w-2xl space-y-6">
    <PageHeader
      :breadcrumbs="[{ label: 'Databases', to: '/databases' }, { label: 'New database' }]"
      title="New database"
      description="Pick any server — MySQL, MariaDB, or PostgreSQL — and an owner. One click creates everything."
    />

    <AppAlert v-if="!canWrite" tone="info">
      Your account has read-only access to databases.
      <RouterLink to="/databases" class="ml-1 underline underline-offset-2">Back to databases</RouterLink>
    </AppAlert>

    <AppCard v-else>
      <form class="space-y-5" novalidate @submit.prevent="submit">
        <FormField label="Name" :hint="NAME_HINT" :error="nameError">
          <AppInput v-model="name" autocomplete="off" spellcheck="false" :invalid="!!nameError" :disabled="submitted" />
        </FormField>

        <FormField label="Server" hint="The engine is decided by the server you pick." :error="serverError">
          <Combobox v-model="serverId" :disabled="submitted">
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

        <FormField label="User" hint="The login that owns the new database.">
          <Combobox v-model="owner" :disabled="submitted">
            <ComboboxAnchor as-child>
              <ComboboxTrigger placeholder="Select user" :label="ownerTriggerLabel(owner)" />
            </ComboboxAnchor>
            <ComboboxList>
              <ComboboxInput placeholder="Search users…" />
              <ComboboxEmpty>Select a server first</ComboboxEmpty>
              <ComboboxGroup>
                <ComboboxItem :value="CREATE_NEW_USER" text-value="Create a new user">
                  Create a new user<ComboboxItemIndicator />
                </ComboboxItem>
                <ComboboxItem
                  v-for="user in ownerOptions"
                  :key="user.id"
                  :value="user.id"
                  :text-value="userLabel(user)"
                >
                  {{ userLabel(user) }}<ComboboxItemIndicator />
                </ComboboxItem>
              </ComboboxGroup>
            </ComboboxList>
          </Combobox>
        </FormField>

        <template v-if="creatingUser">
          <FormField label="Login" :hint="NAME_HINT" :error="newUserError">
            <AppInput
              v-model="newUserName"
              autocomplete="off"
              spellcheck="false"
              :invalid="!!newUserError"
              :disabled="submitted"
            />
          </FormField>
          <FormField
            v-if="hostVisible"
            label="Host"
            hint="Where this user may connect from: localhost, %, or a specific address."
            :error="hostError"
          >
            <AppInput
              v-model="newUserHost"
              autocomplete="off"
              spellcheck="false"
              :invalid="!!hostError"
              :disabled="submitted"
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
        </template>

        <div v-if="submitted" class="space-y-2">
          <AppAlert tone="info">
            {{ stage === 'user' ? `Creating user ${newUserName}…` : `Creating database ${name}…` }}
          </AppAlert>
          <JobProgress
            v-if="stage === 'user' && userRunner.progress.value"
            :event="userRunner.progress.value"
            v-bind="progressProps(userRunner)"
          />
          <JobProgress
            v-if="stage === 'database' && databaseRunner.progress.value"
            :event="databaseRunner.progress.value"
            v-bind="progressProps(databaseRunner)"
          />
        </div>
        <JobFailureNotice v-if="userRunner.error.value" v-bind="failureProps(userRunner)" />
        <JobFailureNotice v-if="databaseRunner.error.value" v-bind="failureProps(databaseRunner)" />

        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <RouterLink to="/databases"><AppButton :disabled="submitted">Cancel</AppButton></RouterLink>
          <AppButton variant="primary" type="submit" :loading="submitted">Create database</AppButton>
        </div>
      </form>
    </AppCard>
  </section>
</template>
