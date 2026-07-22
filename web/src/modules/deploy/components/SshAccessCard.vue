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
  AppInput,
  AppTextarea,
  CredentialReveal,
  type Fact,
  FactList,
  FormField,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'
import CopyField from '@/shared/ui/CopyField.vue'

import {
  addSSHKey,
  disableSSHAccess,
  enableSSHAccess,
  generateSSHKey,
  getSSHAccess,
  removeSSHKey,
  type SshKey,
} from '../api'

const props = defineProps<{ siteId: string }>()

const identity = useIdentityStore()
const { push } = useToasts()
const canWrite = computed(() => identity.can('deploy.write'))

const accessQuery = useQuery({
  queryKey: ['deploy-ssh', () => props.siteId],
  queryFn: () => getSSHAccess(props.siteId),
  enabled: computed(() => !!props.siteId),
  retry: false,
})
const access = computed(() => accessQuery.data.value)
const keys = computed(() => access.value?.keys ?? [])

// The generated private half is held in memory only, and only until it is
// dismissed — nothing on the server can hand it out a second time.
const revealedPrivateKey = ref('')
const revealedLabel = ref('')

const busy = ref('')
const actionError = ref('')
const formError = ref('')
const disableOpen = ref(false)
const removing = ref<SshKey>()

const form = ref({ label: '', publicKey: '' })
const generateLabel = ref('')

const connectionFacts = computed<Fact[]>(() => {
  const current = access.value
  if (!current) return []
  return [
    { label: 'Username', value: current.username },
    { label: 'Host', value: current.host },
    { label: 'Port', value: String(current.port) },
    { label: 'Login shell', value: current.shell },
  ]
})

const connectCommand = computed(() => {
  const current = access.value
  if (!current) return ''
  const port = current.port === 22 ? '' : `-p ${current.port} `
  return `ssh ${port}${current.username}@${current.host}`
})

async function run(key: string, action: () => Promise<unknown>, failure: string) {
  if (!canWrite.value || busy.value) return
  busy.value = key
  actionError.value = ''
  try {
    await action()
    await accessQuery.refetch()
  } catch (caught) {
    actionError.value = caught instanceof Error ? caught.message : failure
  } finally {
    busy.value = ''
  }
}

function enable() {
  return run(
    'toggle',
    async () => {
      await enableSSHAccess(props.siteId)
      push({ tone: 'success', title: 'SSH access enabled' })
    },
    'SSH access could not be enabled.',
  )
}

function disable() {
  disableOpen.value = false
  return run(
    'toggle',
    async () => {
      revealedPrivateKey.value = ''
      await disableSSHAccess(props.siteId)
      push({ tone: 'success', title: 'SSH access disabled' })
    },
    'SSH access could not be disabled.',
  )
}

// The same prefix check the server runs, done here so an obvious paste mistake
// is reported without a round trip. The server remains the authority.
function addKey() {
  formError.value = ''
  const label = form.value.label.trim()
  const publicKey = form.value.publicKey.trim().replace(/\s+/g, ' ')
  if (!label) {
    formError.value = 'Give the key a label so it can be told apart later.'
    return
  }
  // The algorithms listed here are exactly the ones the node installs; a type
  // outside the list is refused by the server too, so accepting it here would
  // only move the rejection one round trip later.
  if (!/^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com) [A-Za-z0-9+/=]+( .*)?$/.test(publicKey)) {
    formError.value = 'Paste one OpenSSH public key line, starting with its algorithm (for example ssh-ed25519 AAAA…).'
    return
  }
  return run(
    'add',
    async () => {
      await addSSHKey(props.siteId, label, publicKey)
      form.value = { label: '', publicKey: '' }
      push({ tone: 'success', title: 'Key added' })
    },
    'The key could not be added.',
  )
}

function generate() {
  formError.value = ''
  const label = generateLabel.value.trim() || 'Generated key'
  return run(
    'generate',
    async () => {
      const generated = await generateSSHKey(props.siteId, label)
      revealedPrivateKey.value = generated.privateKey
      revealedLabel.value = generated.key.label
      generateLabel.value = ''
      push({ tone: 'success', title: 'Key generated — save the private half now' })
    },
    'The key pair could not be generated.',
  )
}

function confirmRemove() {
  const key = removing.value
  removing.value = undefined
  if (!key) return
  return run(
    key.id,
    async () => {
      await removeSSHKey(props.siteId, key.id)
      push({ tone: 'success', title: `Key "${key.label}" removed` })
    },
    'The key could not be removed.',
  )
}
</script>

<template>
  <AppCard eyebrow="Manage" title="SSH access">
    <template #actions>
      <StatusPill
        v-if="access"
        :tone="access.enabled ? 'accent' : 'info'"
        :label="access.enabled ? 'Enabled' : 'Disabled'"
        :pulse="false"
      />
    </template>

    <div class="space-y-4">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        Give this site's own system account (<code class="font-mono text-accent-200">{{ access?.username }}</code
        >) an interactive, key-only SSH login for deployments. Passwords are never accepted, and the account reaches
        nothing outside its own site.
      </p>

      <AppAlert v-if="actionError" tone="danger">{{ actionError }}</AppAlert>

      <div v-if="accessQuery.isPending.value" class="space-y-1" aria-hidden="true">
        <SkeletonRow v-for="n in 2" :key="n" />
      </div>
      <AppAlert v-else-if="accessQuery.isError.value" tone="danger">
        <p>SSH access could not be loaded.</p>
        <AppButton size="sm" class="mt-2" @click="accessQuery.refetch()">Retry</AppButton>
      </AppAlert>

      <template v-else-if="access">
        <!-- The generated private half, shown once and then gone for good -->
        <CredentialReveal
          v-if="revealedPrivateKey"
          :credential="revealedPrivateKey"
          :account-label="revealedLabel"
          @clear="revealedPrivateKey = ''"
        />

        <template v-if="access.enabled">
          <FactList :facts="connectionFacts" />
          <CopyField label="Connect" :value="connectCommand" hint="Authenticate with one of the keys listed below." />
          <p class="text-[12px] text-ink-muted">
            Enabled {{ access.enabledAt ? formatDateTime(access.enabledAt) : 'just now' }}. Authorized keys live at
            <code class="font-mono">{{ access.authorizedKeysPath }}</code
            >, outside the site's own files, so a compromised site cannot add one.
          </p>
        </template>
        <AppAlert v-else tone="info">
          The account keeps a no-login shell until access is enabled. Keys can be prepared now — they are installed on
          the node the moment access is turned on.
        </AppAlert>

        <div class="overflow-hidden rounded-xl border border-outline">
          <table class="w-full text-sm">
            <thead class="bg-raised/50 text-left text-[11px] font-semibold tracking-[0.1em] text-ink-muted uppercase">
              <tr>
                <th class="px-4 py-3">Label</th>
                <th class="px-4 py-3">Algorithm</th>
                <th class="px-4 py-3">Fingerprint</th>
                <th class="px-4 py-3">Added</th>
                <th class="w-20 px-4 py-3 text-right">Remove</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-outline">
              <tr v-if="!keys.length">
                <td colspan="5" class="px-4 py-10 text-center text-ink-muted">
                  No keys installed yet. Until one is added, nobody can log in.
                </td>
              </tr>
              <tr v-for="key in keys" v-else :key="key.id" class="hover:bg-raised/30">
                <td class="px-4 py-3 text-ink">{{ key.label }}</td>
                <td class="px-4 py-3 font-mono text-[12px] text-ink-secondary">{{ key.algorithm }}</td>
                <td class="px-4 py-3 font-mono text-[12px] text-ink-secondary">{{ key.fingerprint }}</td>
                <td class="px-4 py-3 text-ink-muted">{{ formatDateTime(key.createdAt) }}</td>
                <td class="px-4 py-3 text-right">
                  <AppButton
                    size="sm"
                    variant="ghost"
                    icon="trash"
                    title="Remove key"
                    :disabled="!canWrite || !!busy"
                    :loading="busy === key.id"
                    @click="removing = key"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <template v-if="canWrite">
          <div class="space-y-4 border-t border-outline pt-4">
            <p class="text-sm font-medium text-ink">Add a key</p>
            <FormField label="Label" hint="Names the person or machine this key belongs to.">
              <AppInput v-model="form.label" placeholder="e.g. Deploy runner" />
            </FormField>
            <FormField label="Public key" hint="One OpenSSH line — the contents of a .pub file.">
              <AppTextarea
                v-model="form.publicKey"
                rows="3"
                placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5… you@laptop"
              />
            </FormField>
            <AppAlert v-if="formError" tone="danger">{{ formError }}</AppAlert>
            <div class="flex justify-end">
              <AppButton variant="primary" icon="plus" :disabled="!!busy" :loading="busy === 'add'" @click="addKey">
                Add key
              </AppButton>
            </div>
          </div>

          <div class="space-y-4 border-t border-outline pt-4">
            <p class="text-sm font-medium text-ink">Generate a key pair</p>
            <p class="text-[13px] leading-relaxed text-ink-secondary">
              The pair is generated on the node. The public half is installed here; the private half is shown once, in
              the response to this action, and is never stored — lose it and you generate another.
            </p>
            <FormField label="Label">
              <AppInput v-model="generateLabel" placeholder="e.g. CI deploy key" />
            </FormField>
            <div class="flex justify-end">
              <AppButton
                variant="secondary"
                icon="key-round"
                :disabled="!!busy"
                :loading="busy === 'generate'"
                @click="generate"
              >
                Generate a key
              </AppButton>
            </div>
          </div>

          <div class="border-t border-outline pt-4">
            <AppButton
              v-if="access.enabled"
              variant="ghost"
              icon="power"
              :disabled="!!busy"
              @click="disableOpen = true"
            >
              Disable SSH access
            </AppButton>
            <AppButton v-else variant="primary" icon="terminal" :loading="busy === 'toggle'" @click="enable">
              Enable SSH access
            </AppButton>
          </div>
        </template>
        <AppAlert v-else tone="info">An administrator is required to change SSH access.</AppAlert>
      </template>

      <AppConfirmDialog
        :open="disableOpen"
        title="Disable SSH access"
        confirm-label="Disable SSH"
        :busy="busy === 'toggle'"
        @confirm="disable"
        @close="disableOpen = false"
      >
        The account returns to a no-login shell and its authorized-keys file is removed. Anyone connected through it is
        cut off; the keys listed here are kept and re-installed if you enable access again.
      </AppConfirmDialog>

      <AppConfirmDialog
        :open="!!removing"
        title="Remove this key"
        confirm-label="Remove key"
        :busy="!!busy"
        @confirm="confirmRemove"
        @close="removing = undefined"
      >
        “{{ removing?.label }}” ({{ removing?.fingerprint }}) can no longer log in to this site once it is removed.
      </AppConfirmDialog>
    </div>
  </AppCard>
</template>
