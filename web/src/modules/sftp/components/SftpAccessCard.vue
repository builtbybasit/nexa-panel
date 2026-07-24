<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useToasts } from '@/shared/composables/useToasts'
import { formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  type Fact,
  FactList,
  CredentialReveal,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

import { disableSftp, enableSftp, getSftpAccess, resetSftpPassword, type SftpCredential } from '../api'

const props = defineProps<{ siteId: string }>()

const identity = useIdentityStore()
const { push } = useToasts()
const canApply = computed(() => identity.can('operations.apply'))

const accessQuery = useQuery({
  queryKey: ['sftp-access', () => props.siteId],
  queryFn: () => getSftpAccess(props.siteId),
  enabled: computed(() => !!props.siteId),
  retry: false,
})
const access = computed(() => accessQuery.data.value)

// The generated password is held only in memory and only until the operator
// dismisses the reveal — it is never persisted or re-fetchable.
const revealed = ref<SftpCredential>()
const busy = ref(false)
const actionError = ref('')
const disableOpen = ref(false)

const connectionFacts = computed<Fact[]>(() => {
  const current = access.value
  if (!current) return []
  return [
    { label: 'Protocol', value: 'SFTP (SSH)' },
    { label: 'Host', value: current.host },
    { label: 'Port', value: String(current.port) },
    { label: 'Username', value: current.username },
  ]
})

const revealFacts = computed<Fact[]>(() => {
  const credential = revealed.value
  if (!credential) return []
  return [
    { label: 'Protocol', value: 'SFTP (SSH)' },
    { label: 'Host', value: credential.host },
    { label: 'Port', value: String(credential.port) },
    { label: 'Username', value: credential.username },
  ]
})

async function run(action: () => Promise<unknown>, failure: string) {
  if (!canApply.value || busy.value) return
  busy.value = true
  actionError.value = ''
  try {
    await action()
    await accessQuery.refetch()
  } catch (caught) {
    actionError.value = caught instanceof Error ? caught.message : failure
  } finally {
    busy.value = false
  }
}

function enable() {
  return run(async () => {
    revealed.value = await enableSftp(props.siteId)
    push({ tone: 'success', title: 'SFTP access enabled' })
  }, 'SFTP could not be enabled.')
}

function reset() {
  return run(async () => {
    revealed.value = await resetSftpPassword(props.siteId)
    push({ tone: 'success', title: 'A new SFTP password was generated' })
  }, 'The SFTP password could not be reset.')
}

function disable() {
  disableOpen.value = false
  return run(async () => {
    revealed.value = undefined
    await disableSftp(props.siteId)
    push({ tone: 'success', title: 'SFTP access disabled' })
  }, 'SFTP could not be disabled.')
}
</script>

<template>
  <AppCard eyebrow="Manage" title="SFTP access">
    <template #actions>
      <StatusPill
        v-if="access"
        :tone="access.enabled ? 'accent' : access.pendingActivation ? 'warning' : 'info'"
        :label="access.enabled ? 'Enabled' : access.pendingActivation ? 'Pending activation' : 'Disabled'"
        :pulse="false"
      />
    </template>

    <div class="space-y-4">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        Give this site its own SFTP login, jailed to its home directory over SSH. Access uses the site's own system
        account (<code class="font-mono text-accent-200">{{ access?.username }}</code>) — no shell, and no reach into any
        other site. Passwords are generated and shown once.
      </p>

      <AppAlert v-if="actionError" tone="danger">{{ actionError }}</AppAlert>

      <div v-if="accessQuery.isPending.value" class="space-y-1" aria-hidden="true">
        <SkeletonRow v-for="n in 2" :key="n" />
      </div>
      <AppAlert v-else-if="accessQuery.isError.value" tone="danger">
        <p>SFTP access could not be loaded.</p>
        <AppButton size="sm" class="mt-2" @click="accessQuery.refetch()">Retry</AppButton>
      </AppAlert>

      <template v-else-if="access">
        <!-- Freshly generated credential, shown once -->
        <CredentialReveal
          v-if="revealed"
          :credential="revealed.password"
          :account-label="revealed.username"
          :facts="revealFacts"
          @clear="revealed = undefined"
        />

        <!-- Enabled: connection details + management -->
        <template v-else-if="access.enabled">
          <FactList :facts="connectionFacts" />
          <p v-if="access.passwordSetAt" class="text-[12px] text-ink-muted">
            Password last set {{ formatDateTime(access.passwordSetAt) }}. It is not stored — reset it to get a new one.
          </p>
          <div v-if="canApply" class="flex flex-wrap gap-2 border-t border-outline pt-4">
            <AppButton variant="secondary" icon="key-round" :loading="busy" @click="reset">Reset password</AppButton>
            <AppButton variant="ghost" icon="power" :disabled="busy" @click="disableOpen = true">Disable SFTP</AppButton>
          </div>
          <AppAlert v-else tone="info">An administrator is required to change SFTP access.</AppAlert>
        </template>

        <!-- Disabled: offer to enable -->
        <template v-else>
          <AppAlert v-if="access.pendingActivation" tone="info">
            The credentials chosen when this site was created are staged. SFTP turns on automatically the moment the
            site is activated — or enable it here to replace them with a freshly generated password.
          </AppAlert>
          <AppButton v-if="canApply" variant="primary" icon="server" :loading="busy" @click="enable">
            Enable SFTP access
          </AppButton>
          <AppAlert v-else-if="!access.pendingActivation" tone="info">SFTP is disabled. An administrator can enable it.</AppAlert>
        </template>
      </template>

      <AppConfirmDialog
        :open="disableOpen"
        title="Disable SFTP access"
        confirm-label="Disable SFTP"
        :busy="busy"
        @confirm="disable"
        @close="disableOpen = false"
      >
        The SFTP login is removed and the account password is locked. Anyone using it to connect will be cut off.
      </AppConfirmDialog>
    </div>
  </AppCard>
</template>
