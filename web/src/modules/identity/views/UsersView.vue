<script setup lang="ts">
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppInput,
  AppSelect,
  EmptyState,
  FormField,
  PageHeader,
  StatusPill,
} from '@/shared/ui'

import { listSites } from '../../sites/api'
import {
  createUser,
  deleteUser,
  listUsers,
  replaceUserSites,
  updateUser,
  type ManagedRole,
  type ManagedUser,
} from '../api'
import { useIdentityStore } from '../store'

const roleOptions: { value: ManagedRole; label: string }[] = [
  { value: 'admin', label: 'Administrator' },
  { value: 'operator', label: 'Operator' },
  { value: 'developer', label: 'Developer' },
  { value: 'viewer', label: 'Viewer' },
]

const roleTones = { admin: 'accent', operator: 'info', developer: 'warning', viewer: 'neutral' } as const

const identity = useIdentityStore()
const queryClient = useQueryClient()

const usersQuery = useQuery({ queryKey: ['users'], queryFn: listUsers, retry: false })
const users = computed(() => usersQuery.data.value ?? [])

type DialogKind = 'create' | 'edit' | 'delete'
const dialog = ref<DialogKind>()
const target = ref<ManagedUser>()
const busy = ref(false)
const dialogError = ref('')

// Create form state
const createUsername = ref('')
const createPassword = ref('')
const createRole = ref<ManagedRole>('viewer')

// Edit form state
const editRole = ref<ManagedRole>('viewer')
const editPassword = ref('')
const editSiteIds = ref<string[]>([])

const sitesQuery = useQuery({
  queryKey: ['sites'],
  queryFn: listSites,
  retry: false,
  enabled: computed(() => dialog.value === 'edit' && editRole.value === 'developer'),
})
const sites = computed(() => sitesQuery.data.value ?? [])

function siteAccessLabel(user: ManagedUser): string {
  if (user.role !== 'developer') return 'All sites'
  const count = user.siteIds?.length ?? 0
  return `${count} ${count === 1 ? 'site' : 'sites'}`
}

function openCreate() {
  createUsername.value = ''
  createPassword.value = ''
  createRole.value = 'viewer'
  dialogError.value = ''
  dialog.value = 'create'
}

function openEdit(user: ManagedUser) {
  target.value = user
  editRole.value = user.role
  editPassword.value = ''
  editSiteIds.value = [...(user.siteIds ?? [])]
  dialogError.value = ''
  dialog.value = 'edit'
}

function openDelete(user: ManagedUser) {
  target.value = user
  dialogError.value = ''
  dialog.value = 'delete'
}

function close() {
  if (busy.value) return
  dialog.value = undefined
  target.value = undefined
}

function toggleSite(id: string) {
  editSiteIds.value = editSiteIds.value.includes(id)
    ? editSiteIds.value.filter((siteId) => siteId !== id)
    : [...editSiteIds.value, id]
}

function sitesChanged(user: ManagedUser): boolean {
  const before = [...(user.siteIds ?? [])].sort()
  const after = [...editSiteIds.value].sort()
  return before.length !== after.length || before.some((id, index) => id !== after[index])
}

async function run(action: () => Promise<void>) {
  busy.value = true
  dialogError.value = ''
  try {
    await action()
    await queryClient.invalidateQueries({ queryKey: ['users'] })
    dialog.value = undefined
    target.value = undefined
  } catch (caught) {
    dialogError.value = caught instanceof Error ? caught.message : 'The user operation failed.'
  } finally {
    busy.value = false
  }
}

const submitCreate = () =>
  run(async () => {
    await createUser({ username: createUsername.value, password: createPassword.value, role: createRole.value })
  })

const submitEdit = () =>
  run(async () => {
    const user = target.value
    if (!user) return
    const patch: { role?: ManagedRole; password?: string } = {}
    if (editRole.value !== user.role) patch.role = editRole.value
    if (editPassword.value) patch.password = editPassword.value
    if (patch.role !== undefined || patch.password !== undefined) await updateUser(user.id, patch)
    if (editRole.value === 'developer' && sitesChanged(user)) await replaceUserSites(user.id, editSiteIds.value)
  })

const submitDelete = () =>
  run(async () => {
    if (target.value) await deleteUser(target.value.id)
  })
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Access control"
      title="Users"
      description="Panel accounts with role-scoped permissions. Developers only reach the sites granted to them; every account enrolls MFA on first sign-in."
    >
      <AppButton icon="refresh-cw" :loading="usersQuery.isFetching.value" @click="usersQuery.refetch()">Refresh</AppButton>
      <AppButton variant="primary" icon="users" @click="openCreate">Create user</AppButton>
    </PageHeader>

    <AppAlert v-if="usersQuery.isPending.value" tone="info">Loading panel accounts…</AppAlert>
    <AppAlert v-else-if="usersQuery.isError.value" tone="danger">
      The user list is unavailable or your role cannot manage accounts.
    </AppAlert>

    <AppCard v-else flush>
      <div v-if="users.length" class="overflow-x-auto px-2 pb-2 sm:px-3 sm:pb-3">
        <table class="w-full border-collapse text-left">
          <thead>
            <tr class="border-b border-outline">
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">User</th>
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Role</th>
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">MFA</th>
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Last login</th>
              <th class="px-3 py-2.5 text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Site access</th>
              <th class="px-3 py-2.5"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-outline">
            <tr v-for="user in users" :key="user.id">
              <td class="px-3 py-3">
                <strong class="block text-[13px] font-semibold text-ink">{{ user.username }}</strong>
                <small class="block text-[11px] whitespace-nowrap text-ink-muted">Created {{ formatDateTime(user.createdAt) }}</small>
              </td>
              <td class="px-3 py-3">
                <StatusPill :tone="roleTones[user.role] ?? 'neutral'" :label="user.role" :pulse="false" />
              </td>
              <td class="px-3 py-3">
                <StatusPill
                  :tone="user.mfaConfirmed ? 'success' : 'warning'"
                  :label="user.mfaConfirmed ? 'Enrolled' : 'Pending'"
                  :pulse="false"
                />
              </td>
              <td class="px-3 py-3 text-[13px] whitespace-nowrap text-ink-secondary">
                {{ user.lastLoginAt ? formatDateTime(user.lastLoginAt) : 'Never' }}
              </td>
              <td class="px-3 py-3 text-[13px] whitespace-nowrap text-ink-secondary">{{ siteAccessLabel(user) }}</td>
              <td class="px-3 py-3">
                <div class="flex justify-end gap-2">
                  <AppButton size="sm" @click="openEdit(user)">Edit</AppButton>
                  <AppButton v-if="identity.user?.id !== user.id" size="sm" variant="danger" @click="openDelete(user)">
                    Delete
                  </AppButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState
        v-else
        icon="users"
        title="No users"
        description="Create operator, developer, or viewer accounts with least-privilege roles."
        class="m-5"
      />
    </AppCard>

    <div v-if="dialog" class="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true">
      <div class="absolute inset-0 bg-canvas/70 backdrop-blur-sm" aria-hidden="true" @click="close" />
      <div class="relative w-full max-w-md">
        <AppCard v-if="dialog === 'create'" eyebrow="New account" title="Create user">
          <form class="space-y-4" @submit.prevent="submitCreate">
            <FormField label="Username" hint="3–64 characters: letters, digits, dots, hyphens, and underscores.">
              <AppInput
                v-model="createUsername"
                pattern="[A-Za-z0-9][A-Za-z0-9._\-]{2,63}"
                maxlength="64"
                autocomplete="off"
                required
              />
            </FormField>
            <FormField label="Password" hint="At least 12 characters. The user enrolls MFA on first sign-in.">
              <AppInput v-model="createPassword" type="password" minlength="12" autocomplete="new-password" required />
            </FormField>
            <FormField label="Role">
              <AppSelect v-model="createRole">
                <option v-for="option in roleOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
              </AppSelect>
            </FormField>
            <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
            <div class="flex justify-end gap-2">
              <AppButton :disabled="busy" @click="close">Cancel</AppButton>
              <AppButton variant="primary" type="submit" :loading="busy">Create user</AppButton>
            </div>
          </form>
        </AppCard>

        <AppCard v-else-if="dialog === 'edit' && target" eyebrow="Account" :title="`Edit ${target.username}`">
          <form class="space-y-4" @submit.prevent="submitEdit">
            <FormField label="Role" hint="Changing the role revokes the user's active sessions.">
              <AppSelect v-model="editRole">
                <option v-for="option in roleOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
              </AppSelect>
            </FormField>
            <FormField label="New password" hint="Leave blank to keep the current password. Resetting revokes active sessions.">
              <AppInput v-model="editPassword" type="password" minlength="12" autocomplete="new-password" placeholder="Unchanged" />
            </FormField>
            <FormField
              v-if="editRole === 'developer'"
              label="Assigned sites"
              hint="Developers can only see and work on the sites granted here."
            >
              <div class="max-h-48 space-y-0.5 overflow-y-auto rounded-lg border border-outline-strong bg-canvas/60 p-1.5">
                <label
                  v-for="site in sites"
                  :key="site.id"
                  class="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] hover:bg-white/[0.04]"
                >
                  <input
                    type="checkbox"
                    class="accent-accent-500"
                    :checked="editSiteIds.includes(site.id)"
                    @change="toggleSite(site.id)"
                  />
                  <span class="min-w-0 flex-1 truncate text-ink">{{ site.displayName }}</span>
                  <span class="shrink-0 font-mono text-[11px] text-ink-muted">{{ site.primaryDomain }}</span>
                </label>
                <p v-if="sitesQuery.isPending.value" class="px-2 py-1.5 text-xs text-ink-muted">Loading sites…</p>
                <p v-else-if="sitesQuery.isError.value" class="px-2 py-1.5 text-xs text-rose-300">The site list is unavailable.</p>
                <p v-else-if="!sites.length" class="px-2 py-1.5 text-xs text-ink-muted">No sites exist yet.</p>
              </div>
            </FormField>
            <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
            <div class="flex justify-end gap-2">
              <AppButton :disabled="busy" @click="close">Cancel</AppButton>
              <AppButton variant="primary" type="submit" :loading="busy">Save changes</AppButton>
            </div>
          </form>
        </AppCard>

        <AppCard v-else-if="dialog === 'delete' && target" eyebrow="Irreversible" title="Delete user">
          <div class="space-y-4">
            <p class="text-[13px] leading-relaxed text-ink-secondary">
              Delete <strong class="font-semibold text-ink">{{ target.username }}</strong>? Their sessions and site grants are
              revoked immediately. This cannot be undone.
            </p>
            <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
            <div class="flex justify-end gap-2">
              <AppButton :disabled="busy" @click="close">Cancel</AppButton>
              <AppButton variant="danger" :loading="busy" @click="submitDelete">Delete user</AppButton>
            </div>
          </div>
        </AppCard>
      </div>
    </div>
  </section>
</template>
