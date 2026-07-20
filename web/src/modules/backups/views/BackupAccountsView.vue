<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, reactive, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'
import { useToasts } from '@/shared/composables/useToasts'
import {
  AppAlert,
  AppButton,
  AppConfirmDialog,
  AppDialog,
  AppIcon,
  AppInput,
  AppSelect,
  EmptyState,
  FormField,
  PageHeader,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

import {
  createBackupAccount,
  deleteBackupAccount,
  listBackupAccounts,
  testBackupAccount,
  updateBackupAccount,
  type BackupAccount,
  type BackupAccountRequest,
  type BackupAccountType,
} from '../api'
import BackupsTabs from './BackupsTabs.vue'

const { push } = useToasts()
const identity = useIdentityStore()
const canWrite = computed(() => identity.can('backups.write'))

const accountsQuery = useQuery({ queryKey: ['backup-accounts'], queryFn: listBackupAccounts, retry: false })
const accounts = computed(() => accountsQuery.data.value ?? [])

// --- storage type catalog --------------------------------------------------
// Each backup account is an rclone remote. A field's key is either the literal
// rclone param name (stored in config, or in secret when `secret` is set) or
// the special `__path` sentinel bound to the account's top-level Path.
interface AccountField {
  key: string
  label: string
  kind?: 'text' | 'password' | 'select' | 'number'
  secret?: boolean
  required?: boolean
  hint?: string
  placeholder?: string
  options?: { value: string; label: string }[]
}

const typeCatalog: { value: BackupAccountType; label: string; icon: string; note?: string; fields: AccountField[] }[] = [
  {
    value: 'local',
    label: 'Local',
    icon: 'hard-drive',
    fields: [
      { key: '__path', label: 'Path', required: true, hint: 'An absolute directory on this server.', placeholder: '/var/backups/nexa' },
    ],
  },
  {
    value: 's3',
    label: 'S3-compatible',
    icon: 'cloud',
    note: 'MinIO, AWS S3, or any S3-compatible object storage.',
    fields: [
      {
        key: 'provider',
        label: 'Provider',
        kind: 'select',
        required: true,
        options: [
          { value: 'Minio', label: 'MinIO' },
          { value: 'AWS', label: 'AWS S3' },
          { value: 'Other', label: 'Other S3-compatible' },
        ],
      },
      { key: 'endpoint', label: 'Storage URL', required: true, hint: 'The S3 endpoint, including scheme.', placeholder: 'https://10.0.0.5:9000' },
      { key: 'region', label: 'Region', hint: 'Optional.', placeholder: 'us-east-1' },
      { key: 'access_key_id', label: 'Access key identifier', required: true },
      { key: 'secret_access_key', label: 'Secret access key', kind: 'password', secret: true, required: true },
      { key: '__path', label: 'Bucket', required: true, hint: 'A bucket name, optionally with a sub-path.', placeholder: 'backups/nightly' },
    ],
  },
  {
    value: 'sftp',
    label: 'SFTP / SCP',
    icon: 'server',
    fields: [
      { key: 'host', label: 'Host', required: true, placeholder: '10.0.0.9' },
      { key: 'port', label: 'Port', kind: 'number', placeholder: '22' },
      { key: 'user', label: 'User', required: true, placeholder: 'backup' },
      { key: 'pass', label: 'Password', kind: 'password', secret: true },
      { key: '__path', label: 'Path', hint: 'The remote directory backups are written to.', placeholder: 'backups' },
    ],
  },
  {
    value: 'drive',
    label: 'Google Drive',
    icon: 'cloud',
    note: 'Obtain the OAuth token JSON with `rclone config` and paste it below.',
    fields: [
      { key: 'client_id', label: 'Client ID', hint: 'Optional — your own Google OAuth client.' },
      { key: 'token', label: 'OAuth token (JSON)', kind: 'password', secret: true, hint: 'The token object from rclone config.' },
      { key: 'root_folder_id', label: 'Root folder ID', hint: 'Optional.' },
      { key: '__path', label: 'Path', hint: 'A sub-folder for backups.', placeholder: 'backups' },
    ],
  },
]

function catalogFor(type: BackupAccountType) {
  return typeCatalog.find((entry) => entry.value === type) ?? typeCatalog[0]!
}

// --- create / edit dialog --------------------------------------------------
const dialogOpen = ref(false)
const editing = ref<BackupAccount>()
const saving = ref(false)
const formError = ref('')

const form = reactive<{ name: string; type: BackupAccountType; values: Record<string, string> }>({
  name: '',
  type: 'local',
  values: {},
})

const activeCatalog = computed(() => catalogFor(form.type))

function openCreate() {
  if (!canWrite.value) return
  editing.value = undefined
  formError.value = ''
  form.name = ''
  form.type = 'local'
  form.values = {}
  dialogOpen.value = true
}

function openEdit(account: BackupAccount) {
  if (!canWrite.value) return
  editing.value = account
  formError.value = ''
  form.name = account.name
  form.type = account.type
  // Prefill config + path; secrets are never returned, so their fields start
  // blank and only overwrite when the user types something.
  const values: Record<string, string> = { ...account.config, __path: account.path }
  form.values = values
  dialogOpen.value = true
}

function buildRequest(): BackupAccountRequest | undefined {
  const catalog = activeCatalog.value
  const config: Record<string, string> = {}
  const secret: Record<string, string> = {}
  let path = ''
  for (const field of catalog.fields) {
    const raw = (form.values[field.key] ?? '').trim()
    if (field.key === '__path') {
      path = raw
    } else if (field.secret) {
      if (raw) secret[field.key] = raw
    } else if (raw) {
      config[field.key] = raw
    }
    if (field.required && !raw) {
      // Secret fields are only required on create; on edit a blank keeps the stored value.
      if (field.secret && editing.value?.hasSecret) continue
      formError.value = `${field.label} is required.`
      return undefined
    }
  }
  if (!form.name.trim()) {
    formError.value = 'Name is required.'
    return undefined
  }
  const request: BackupAccountRequest = { name: form.name.trim(), type: form.type, path, config }
  if (Object.keys(secret).length) request.secret = secret
  return request
}

async function save() {
  if (!canWrite.value) return
  formError.value = ''
  const request = buildRequest()
  if (!request) return
  saving.value = true
  try {
    if (editing.value) {
      await updateBackupAccount(editing.value.id, request)
      push({ title: 'Backup account updated', tone: 'success' })
    } else {
      await createBackupAccount(request)
      push({ title: 'Backup account created', tone: 'success' })
    }
    dialogOpen.value = false
    await accountsQuery.refetch()
  } catch (caught) {
    formError.value = caught instanceof Error ? caught.message : 'The backup account could not be saved.'
  } finally {
    saving.value = false
  }
}

// --- test ------------------------------------------------------------------
const testingId = ref<string>()

async function runTest(account: BackupAccount) {
  if (!canWrite.value) return
  testingId.value = account.id
  try {
    const result = await testBackupAccount(account.id)
    push({
      title: result.ok ? `${account.name} is reachable` : `${account.name} failed`,
      body: result.message,
      tone: result.ok ? 'success' : 'danger',
    })
  } catch (caught) {
    push({ title: `Could not test ${account.name}`, body: caught instanceof Error ? caught.message : undefined, tone: 'danger' })
  } finally {
    testingId.value = undefined
  }
}

// --- delete ----------------------------------------------------------------
const confirmDelete = ref<BackupAccount>()
const deleting = ref(false)

async function doDelete() {
  if (!canWrite.value) return
  const account = confirmDelete.value
  if (!account) return
  deleting.value = true
  try {
    await deleteBackupAccount(account.id)
    push({ title: `${account.name} removed`, tone: 'success' })
    confirmDelete.value = undefined
    await accountsQuery.refetch()
  } catch (caught) {
    push({ title: `Could not remove ${account.name}`, body: caught instanceof Error ? caught.message : undefined, tone: 'danger' })
  } finally {
    deleting.value = false
  }
}

function typeLabel(type: BackupAccountType) {
  return catalogFor(type).label
}
function typeIcon(type: BackupAccountType) {
  return catalogFor(type).icon
}
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Backups"
      title="Backup accounts"
      description="Storage destinations for scheduled and on-demand backups. Accounts can use a local directory or a remote target reached through rclone: S3-compatible storage, SFTP, or Google Drive."
    >
      <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">New account</AppButton>
    </PageHeader>

    <BackupsTabs />

    <div v-if="accountsQuery.isPending.value" class="space-y-2">
      <SkeletonRow v-for="n in 3" :key="n" />
    </div>

    <AppAlert v-else-if="accountsQuery.isError.value" tone="danger">
      <p>The backup accounts couldn't be loaded.</p>
      <AppButton size="sm" class="mt-2" icon="refresh-cw" @click="accountsQuery.refetch()">Retry</AppButton>
    </AppAlert>

    <EmptyState
      v-else-if="!accounts.length"
      icon="archive"
      title="No backup accounts yet"
      description="Create a storage destination to start backing up sites and databases."
    >
      <template #action>
        <AppButton v-if="canWrite" variant="primary" icon="plus" @click="openCreate">New account</AppButton>
      </template>
    </EmptyState>

    <ul v-else class="space-y-2">
      <li
        v-for="account in accounts"
        :key="account.id"
        class="flex flex-wrap items-center gap-4 rounded-xl border border-outline bg-white/[0.02] px-4 py-3"
      >
        <span class="grid size-10 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-ink-muted">
          <AppIcon :name="typeIcon(account.type)" :size="18" />
        </span>
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-semibold text-ink">{{ account.name }}</p>
          <p class="truncate font-mono text-xs text-ink-muted">{{ account.path || '—' }}</p>
        </div>
        <StatusPill tone="neutral" :label="typeLabel(account.type)" :pulse="false" />
        <div v-if="canWrite" class="flex shrink-0 items-center gap-2">
          <AppButton size="sm" icon="plug-zap" :loading="testingId === account.id" @click="runTest(account)">Test</AppButton>
          <AppButton size="sm" icon="pencil" @click="openEdit(account)">Edit</AppButton>
          <AppButton size="sm" variant="danger" icon="trash" @click="confirmDelete = account">Delete</AppButton>
        </div>
      </li>
    </ul>

    <!-- create / edit -->
    <AppDialog
      :open="dialogOpen && canWrite"
      :title="editing ? 'Edit backup account' : 'New backup account'"
      size="lg"
      @close="dialogOpen = false"
    >
      <form class="space-y-4" @submit.prevent="save">
        <FormField label="Name">
          <AppInput v-model="form.name" placeholder="My storage" autocomplete="off" />
        </FormField>

        <FormField label="Type" hint="Changing the type replaces the fields below.">
          <AppSelect v-model="form.type" :disabled="!!editing">
            <option v-for="entry in typeCatalog" :key="entry.value" :value="entry.value">{{ entry.label }}</option>
          </AppSelect>
        </FormField>

        <AppAlert v-if="activeCatalog.note" tone="info">{{ activeCatalog.note }}</AppAlert>

        <FormField
          v-for="field in activeCatalog.fields"
          :key="field.key"
          :label="field.label"
          :hint="field.secret && editing?.hasSecret ? 'Leave blank to keep the current value.' : field.hint"
        >
          <AppSelect
            v-if="field.kind === 'select'"
            :model-value="form.values[field.key] ?? ''"
            @update:model-value="form.values[field.key] = String($event)"
          >
            <option v-for="option in field.options" :key="option.value" :value="option.value">{{ option.label }}</option>
          </AppSelect>
          <AppInput
            v-else
            :model-value="form.values[field.key] ?? ''"
            :type="field.kind === 'password' ? 'password' : field.kind === 'number' ? 'number' : 'text'"
            :placeholder="field.secret && editing?.hasSecret ? '••••••••' : field.placeholder"
            autocomplete="off"
            @update:model-value="form.values[field.key] = String($event)"
          />
        </FormField>

        <AppAlert v-if="formError" tone="danger">{{ formError }}</AppAlert>
      </form>

      <template #footer>
        <AppButton :disabled="saving" @click="dialogOpen = false">Cancel</AppButton>
        <AppButton variant="primary" :loading="saving" @click="save">{{ editing ? 'Save' : 'Create' }}</AppButton>
      </template>
    </AppDialog>

    <AppConfirmDialog
      :open="canWrite && !!confirmDelete"
      title="Delete backup account"
      :confirm-label="`Delete ${confirmDelete?.name ?? ''}`"
      tone="danger"
      :busy="deleting"
      @confirm="doDelete"
      @close="confirmDelete = undefined"
    >
      Removing <strong>{{ confirmDelete?.name }}</strong> deletes its stored credentials. Backups already written to the
      storage are not touched, but plans using this account will need a new destination.
    </AppConfirmDialog>
  </section>
</template>
