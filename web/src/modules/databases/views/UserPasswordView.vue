<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useJobRunner } from '@/shared/composables/useJobRunner'
import {
  AppAlert,
  AppButton,
  AppCard,
  FactList,
  JobFailureNotice,
  JobProgress,
  PageHeader,
  PasswordField,
  type Fact,
} from '@/shared/ui'

import { useIdentityStore } from '@/modules/identity/store'

import { setUserPassword } from '../api'
import { failureProps, progressProps } from '../composables/runnerProps'
import { useDatabasesData } from '../composables/useDatabasesData'
import { userLabel } from '../lib/engines'

const route = useRoute()
const router = useRouter()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('databases.write'))
const userId = computed(() => String(route.params.userId ?? ''))

const data = useDatabasesData()
const user = computed(() => data.user(userId.value))
const missing = computed(() => !data.usersQuery.isPending.value && !data.usersQuery.isError.value && !user.value)

const runner = useJobRunner()
const password = ref('')
const attempted = ref(false)

const passwordError = computed(() =>
  attempted.value && password.value.length < 8 ? 'Use at least 8 characters — or generate one.' : '',
)

const facts = computed<Fact[]>(() => {
  const item = user.value
  if (!item) return []
  const list: Fact[] = [{ label: 'User', value: userLabel(item), mono: true }]
  list.push({ label: 'Server', value: data.serverName(item.serverId) })
  const owned = data.databases.value.filter((database) => database.ownerUserId === item.id)
  if (owned.length) {
    list.push({
      label: owned.length === 1 ? 'Owns database' : 'Owns databases',
      value: owned.map((database) => database.name).join(', '),
    })
  }
  return list
})

async function submit() {
  const item = user.value
  if (!item || !canWrite.value || runner.busy.value) return
  attempted.value = true
  if (passwordError.value) return
  await runner.run(async () => (await setUserPassword(item.id, password.value)).job.id, {
    onSettled: data.refreshAll,
    successToast: `Password updated for ${userLabel(item)}`,
    failureMessage: 'Changing the password failed',
  })
  if (!runner.error.value) await router.push('/databases')
}
</script>

<template>
  <section class="mx-auto max-w-2xl space-y-6">
    <PageHeader
      :breadcrumbs="[{ label: 'Databases', to: '/databases' }, { label: 'Change password' }]"
      :title="user ? `Change password for ${userLabel(user)}` : 'Change password'"
      description="The new password is applied to the server immediately."
    />

    <AppAlert v-if="!canWrite" tone="info">
      Your account has read-only access to databases.
      <RouterLink to="/databases" class="ml-1 underline underline-offset-2">Back to databases</RouterLink>
    </AppAlert>

    <AppAlert v-else-if="missing" tone="danger">
      This user no longer exists.
      <RouterLink to="/databases" class="ml-1 underline underline-offset-2">Back to databases</RouterLink>
    </AppAlert>

    <AppCard v-else-if="user">
      <div class="mb-5"><FactList :facts="facts" /></div>
      <form class="space-y-5" novalidate @submit.prevent="submit">
        <PasswordField
          v-model="password"
          label="New password"
          :minimum-length="8"
          :maximum-length="128"
          :error="passwordError"
          hint="Connected applications fail until they use the new password. Copy it now — it is never shown again."
        />
        <JobFailureNotice v-if="runner.error.value" v-bind="failureProps(runner)" />
        <JobProgress v-if="runner.progress.value" :event="runner.progress.value" v-bind="progressProps(runner)" />
        <div class="flex flex-wrap justify-end gap-2 pt-1">
          <RouterLink to="/databases"><AppButton :disabled="runner.busy.value">Cancel</AppButton></RouterLink>
          <AppButton variant="primary" type="submit" :loading="runner.busy.value">Change password</AppButton>
        </div>
      </form>
    </AppCard>
  </section>
</template>
