<script setup lang="ts">
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { computed, ref, watch } from 'vue'

import { useCollection } from '@/shared/composables/useCollection'
import { formatDateTime } from '@/shared/formatters'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppConfirmDialog,
  AppDialog,
  AppInput,
  AppSelect,
  Checkbox,
  EmptyState,
  FormField,
  ListToolbar,
  PageHeader,
  SkeletonRow,
  StatusPill,
} from '@/shared/ui'

import { listSites } from '../../sites/api'
import {
  createUser,
  deleteUser,
  IdentityRequestError,
  listUsers,
  replaceUserSites,
  updateUser,
  type ManagedRole,
  type ManagedUser,
} from '../api'
import { useIdentityStore } from '../store'

const roleOptions: { value: ManagedRole; label: string }[] = [
  { value: 'admin', label: 'Administrator — full control incl. users' },
  { value: 'operator', label: 'Operator — manage resources, no user admin' },
  { value: 'developer', label: 'Developer — only granted sites, files, logs, tasks' },
  { value: 'viewer', label: 'Viewer — read-only' },
]

const roleTones = { admin: 'accent', operator: 'info', developer: 'warning', viewer: 'neutral' } as const

const identity = useIdentityStore()
const queryClient = useQueryClient()

const usersQuery = useQuery({ queryKey: ['users'], queryFn: listUsers, retry: false })
const users = computed(() => usersQuery.data.value ?? [])

const usersErrorMessage = computed(() => {
  const error = usersQuery.error.value
  if (error instanceof IdentityRequestError && error.status === 403)
    return 'Your account does not have permission to manage users.'
  return 'The user list could not be loaded. Check that the control plane is reachable, then retry.'
})

const {
  search,
  page,
  pageCount,
  items: pagedUsers,
  total,
  matching,
} = useCollection(() => users.value, { searchText: (user) => `${user.username} ${user.role}` })

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

// Site scoping draft shared by the create and edit dialogs (only one is open at a time).
const draftSiteIds = ref<string[]>([])
const siteFilter = ref('')

// Password tooling shared by the create and edit dialogs.
const passwordVisible = ref(false)
const passwordCopied = ref(false)
const passwordNotice = ref<{ text: string; tone: 'info' | 'error' }>()

const scopingRole = computed(() => {
  if (dialog.value === 'create') return createRole.value
  if (dialog.value === 'edit') return editRole.value
  return undefined
})
const listNeedsSiteNames = computed(() =>
  users.value.some((user) => user.role === 'developer' && (user.siteIds?.length ?? 0) > 0),
)
const sitesQuery = useQuery({
  queryKey: ['sites'],
  queryFn: listSites,
  retry: false,
  enabled: computed(() => scopingRole.value === 'developer' || listNeedsSiteNames.value),
})
const sites = computed(() => sitesQuery.data.value ?? [])
const siteSlugById = computed(() => new Map(sites.value.map((site) => [site.id, site.slug])))

const filteredSites = computed(() => {
  const needle = siteFilter.value.trim().toLowerCase()
  if (!needle) return sites.value
  return sites.value.filter((site) =>
    `${site.displayName} ${site.slug} ${site.primaryDomain}`.toLowerCase().includes(needle),
  )
})

function siteAccess(user: ManagedUser): { label: string; title?: string } {
  if (user.role !== 'developer') return { label: 'All sites' }
  const ids = user.siteIds ?? []
  if (!ids.length) return { label: 'No sites' }
  const slugs = ids.map((id) => siteSlugById.value.get(id)).filter((slug): slug is string => slug !== undefined)
  if (slugs.length !== ids.length) return { label: `${ids.length} ${ids.length === 1 ? 'site' : 'sites'}` }
  const extra = slugs.length - 2
  return {
    label: extra > 0 ? `${slugs.slice(0, 2).join(', ')} +${extra} more` : slugs.join(', '),
    title: slugs.join(', '),
  }
}

const PASSWORD_CLASSES = ['abcdefghijklmnopqrstuvwxyz', 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', '0123456789', '!@#$%^&*-_=+']

function randomBelow(bound: number): number {
  const buffer = new Uint32Array(1)
  crypto.getRandomValues(buffer)
  return (buffer[0] ?? 0) % bound
}

/** 20 random characters guaranteed to mix all four classes. */
function generatePassword(length = 20): string {
  const all = PASSWORD_CLASSES.join('')
  const chars = PASSWORD_CLASSES.map((set) => set.charAt(randomBelow(set.length)))
  while (chars.length < length) chars.push(all.charAt(randomBelow(all.length)))
  for (let i = chars.length - 1; i > 0; i--) {
    const j = randomBelow(i + 1)
    ;[chars[i], chars[j]] = [chars[j] ?? '', chars[i] ?? '']
  }
  return chars.join('')
}

function fillGeneratedPassword(field: 'create' | 'edit') {
  const generated = generatePassword()
  if (field === 'create') createPassword.value = generated
  else editPassword.value = generated
  passwordVisible.value = true
  passwordNotice.value = undefined
}

// Editing the password invalidates both the copied state and any copy notice.
watch([createPassword, editPassword], () => {
  passwordCopied.value = false
  passwordNotice.value = undefined
})

async function copyPassword(value: string) {
  try {
    await navigator.clipboard.writeText(value)
    passwordCopied.value = true
    passwordNotice.value = { text: 'Password copied.', tone: 'info' }
  } catch {
    passwordCopied.value = false
    passwordNotice.value = {
      text: 'Copying was blocked by the browser. Show the password and copy it manually.',
      tone: 'error',
    }
  }
}

function resetPasswordTools() {
  passwordVisible.value = false
  passwordCopied.value = false
  passwordNotice.value = undefined
}

function openCreate() {
  createUsername.value = ''
  createPassword.value = ''
  createRole.value = 'viewer'
  draftSiteIds.value = []
  siteFilter.value = ''
  resetPasswordTools()
  dialogError.value = ''
  dialog.value = 'create'
}

function openEdit(user: ManagedUser) {
  target.value = user
  editRole.value = user.role
  editPassword.value = ''
  draftSiteIds.value = [...(user.siteIds ?? [])]
  siteFilter.value = ''
  resetPasswordTools()
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
  draftSiteIds.value = draftSiteIds.value.includes(id)
    ? draftSiteIds.value.filter((siteId) => siteId !== id)
    : [...draftSiteIds.value, id]
}

function selectFilteredSites() {
  draftSiteIds.value = [...new Set([...draftSiteIds.value, ...filteredSites.value.map((site) => site.id)])]
}

function clearSelectedSites() {
  draftSiteIds.value = []
}

function sitesChanged(user: ManagedUser): boolean {
  const before = [...(user.siteIds ?? [])].sort()
  const after = [...draftSiteIds.value].sort()
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
    const created = await createUser({
      username: createUsername.value,
      password: createPassword.value,
      role: createRole.value,
    })
    if (createRole.value === 'developer' && draftSiteIds.value.length) {
      try {
        await replaceUserSites(created.id, draftSiteIds.value)
      } catch {
        await queryClient.invalidateQueries({ queryKey: ['users'] })
        throw new Error(`${created.username} was created, but granting sites failed. Edit the user to grant sites.`)
      }
    }
  })

const submitEdit = () =>
  run(async () => {
    const user = target.value
    if (!user) return
    const patch: { role?: ManagedRole; password?: string } = {}
    if (editRole.value !== user.role) patch.role = editRole.value
    if (editPassword.value) patch.password = editPassword.value
    if (patch.role !== undefined || patch.password !== undefined) await updateUser(user.id, patch)
    if (editRole.value === 'developer' && sitesChanged(user)) await replaceUserSites(user.id, draftSiteIds.value)
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

    <AppCard v-if="usersQuery.isPending.value" flush>
      <div class="space-y-1 p-3">
        <SkeletonRow v-for="n in 3" :key="n" />
      </div>
    </AppCard>

    <AppAlert v-else-if="usersQuery.isError.value" tone="danger">
      <div class="flex flex-wrap items-center gap-3">
        <span class="min-w-0 flex-1">{{ usersErrorMessage }}</span>
        <AppButton size="sm" @click="usersQuery.refetch()">Retry</AppButton>
      </div>
    </AppAlert>

    <template v-else>
      <ListToolbar
        v-if="total > 5"
        v-model:search="search"
        :count="matching"
        count-label="users"
        placeholder="Search by username or role"
      />

      <AppCard flush>
        <div v-if="pagedUsers.length" class="overflow-x-auto px-2 pb-2 sm:px-3 sm:pb-3">
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
              <tr v-for="user in pagedUsers" :key="user.id">
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
                <td
                  class="px-3 py-3 text-[13px] whitespace-nowrap text-ink-secondary"
                  :class="siteAccess(user).title ? 'cursor-help' : ''"
                  :title="siteAccess(user).title"
                >
                  {{ siteAccess(user).label }}
                </td>
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
          <nav v-if="pageCount > 1" class="flex items-center justify-end gap-2 px-3 pt-3" aria-label="Pagination">
            <AppButton size="sm" icon="chevron-left" aria-label="Previous page" :disabled="page <= 1" @click="page--" />
            <span class="text-[13px] text-ink-muted tabular-nums">Page {{ page }} of {{ pageCount }}</span>
            <AppButton size="sm" icon="chevron-right" aria-label="Next page" :disabled="page >= pageCount" @click="page++" />
          </nav>
        </div>
        <EmptyState
          v-else-if="search"
          icon="search"
          title="No matching users"
          :description="`No users match “${search}”.`"
          class="m-5"
        />
        <EmptyState
          v-else
          icon="users"
          title="No users"
          description="Create operator, developer, or viewer accounts with least-privilege roles."
          class="m-5"
        />
      </AppCard>
    </template>

    <AppDialog :open="dialog === 'create'" title="Create user" @close="close">
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
          <div class="flex gap-2">
            <AppInput
              v-model="createPassword"
              :type="passwordVisible ? 'text' : 'password'"
              class="flex-1"
              minlength="12"
              autocomplete="new-password"
              required
            />
            <AppButton
              icon="sparkles"
              aria-label="Generate password"
              title="Generate password"
              @click="fillGeneratedPassword('create')"
            />
            <AppButton
              :icon="passwordVisible ? 'eye-off' : 'eye'"
              :aria-label="passwordVisible ? 'Hide password' : 'Show password'"
              :title="passwordVisible ? 'Hide password' : 'Show password'"
              @click="passwordVisible = !passwordVisible"
            />
            <AppButton
              :icon="passwordCopied ? 'check' : 'copy'"
              aria-label="Copy password"
              title="Copy password"
              :disabled="!createPassword"
              @click="copyPassword(createPassword)"
            />
          </div>
          <p
            aria-live="polite"
            class="mt-1.5 text-xs leading-relaxed"
            :class="passwordNotice?.tone === 'error' ? 'text-rose-300' : 'text-ink-muted'"
          >
            {{ passwordNotice?.text }}
          </p>
        </FormField>
        <FormField label="Role">
          <AppSelect v-model="createRole">
            <option v-for="option in roleOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </AppSelect>
        </FormField>
        <FormField
          v-if="createRole === 'developer'"
          label="Assigned sites"
          hint="Developers can only see and work on the sites granted here."
        >
          <div class="mb-1.5 flex items-center gap-2">
            <AppInput v-model="siteFilter" class="flex-1" placeholder="Filter sites" aria-label="Filter sites" autocomplete="off" />
            <AppButton size="sm" :disabled="!filteredSites.length" @click="selectFilteredSites">Select all</AppButton>
            <AppButton size="sm" :disabled="!draftSiteIds.length" @click="clearSelectedSites">Clear</AppButton>
          </div>
          <div class="max-h-48 space-y-0.5 overflow-y-auto rounded-lg border border-outline-strong bg-canvas/60 p-1.5">
            <label
              v-for="site in filteredSites"
              :key="site.id"
              class="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] hover:bg-white/[0.04]"
            >
              <Checkbox
                :model-value="draftSiteIds.includes(site.id)"
                :aria-label="`Assign ${site.displayName}`"
                @update:model-value="toggleSite(site.id)"
              />
              <span class="min-w-0 flex-1 truncate text-ink">{{ site.displayName }}</span>
              <span class="shrink-0 font-mono text-[11px] text-ink-muted">{{ site.primaryDomain }}</span>
            </label>
            <p v-if="sitesQuery.isPending.value" class="px-2 py-1.5 text-xs text-ink-muted">Loading sites…</p>
            <p v-else-if="sitesQuery.isError.value" class="px-2 py-1.5 text-xs text-rose-300">The site list is unavailable.</p>
            <p v-else-if="!sites.length" class="px-2 py-1.5 text-xs text-ink-muted">No sites exist yet.</p>
            <p v-else-if="!filteredSites.length" class="px-2 py-1.5 text-xs text-ink-muted">No sites match “{{ siteFilter }}”.</p>
          </div>
          <p class="mt-1.5 text-xs text-ink-muted" aria-live="polite">{{ draftSiteIds.length }} selected</p>
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="busy" @click="close">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="busy">Create user</AppButton>
        </div>
      </form>
    </AppDialog>

    <AppDialog :open="dialog === 'edit' && !!target" :title="target ? `Edit ${target.username}` : 'Edit user'" @close="close">
      <form v-if="target" class="space-y-4" @submit.prevent="submitEdit">
        <FormField label="Role" hint="Changing the role revokes the user's active sessions.">
          <AppSelect v-model="editRole">
            <option v-for="option in roleOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </AppSelect>
        </FormField>
        <FormField label="New password" hint="Leave blank to keep the current password. Resetting revokes active sessions.">
          <div class="flex gap-2">
            <AppInput
              v-model="editPassword"
              :type="passwordVisible ? 'text' : 'password'"
              class="flex-1"
              minlength="12"
              autocomplete="new-password"
              placeholder="Unchanged"
            />
            <AppButton
              icon="sparkles"
              aria-label="Generate password"
              title="Generate password"
              @click="fillGeneratedPassword('edit')"
            />
            <AppButton
              :icon="passwordVisible ? 'eye-off' : 'eye'"
              :aria-label="passwordVisible ? 'Hide password' : 'Show password'"
              :title="passwordVisible ? 'Hide password' : 'Show password'"
              @click="passwordVisible = !passwordVisible"
            />
            <AppButton
              :icon="passwordCopied ? 'check' : 'copy'"
              aria-label="Copy password"
              title="Copy password"
              :disabled="!editPassword"
              @click="copyPassword(editPassword)"
            />
          </div>
          <p
            aria-live="polite"
            class="mt-1.5 text-xs leading-relaxed"
            :class="passwordNotice?.tone === 'error' ? 'text-rose-300' : 'text-ink-muted'"
          >
            {{ passwordNotice?.text }}
          </p>
        </FormField>
        <FormField
          v-if="editRole === 'developer'"
          label="Assigned sites"
          hint="Developers can only see and work on the sites granted here."
        >
          <div class="mb-1.5 flex items-center gap-2">
            <AppInput v-model="siteFilter" class="flex-1" placeholder="Filter sites" aria-label="Filter sites" autocomplete="off" />
            <AppButton size="sm" :disabled="!filteredSites.length" @click="selectFilteredSites">Select all</AppButton>
            <AppButton size="sm" :disabled="!draftSiteIds.length" @click="clearSelectedSites">Clear</AppButton>
          </div>
          <div class="max-h-48 space-y-0.5 overflow-y-auto rounded-lg border border-outline-strong bg-canvas/60 p-1.5">
            <label
              v-for="site in filteredSites"
              :key="site.id"
              class="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] hover:bg-white/[0.04]"
            >
              <Checkbox
                :model-value="draftSiteIds.includes(site.id)"
                :aria-label="`Assign ${site.displayName}`"
                @update:model-value="toggleSite(site.id)"
              />
              <span class="min-w-0 flex-1 truncate text-ink">{{ site.displayName }}</span>
              <span class="shrink-0 font-mono text-[11px] text-ink-muted">{{ site.primaryDomain }}</span>
            </label>
            <p v-if="sitesQuery.isPending.value" class="px-2 py-1.5 text-xs text-ink-muted">Loading sites…</p>
            <p v-else-if="sitesQuery.isError.value" class="px-2 py-1.5 text-xs text-rose-300">The site list is unavailable.</p>
            <p v-else-if="!sites.length" class="px-2 py-1.5 text-xs text-ink-muted">No sites exist yet.</p>
            <p v-else-if="!filteredSites.length" class="px-2 py-1.5 text-xs text-ink-muted">No sites match “{{ siteFilter }}”.</p>
          </div>
          <p class="mt-1.5 text-xs text-ink-muted" aria-live="polite">{{ draftSiteIds.length }} selected</p>
        </FormField>
        <AppAlert v-if="dialogError" tone="danger">{{ dialogError }}</AppAlert>
        <div class="flex justify-end gap-2">
          <AppButton :disabled="busy" @click="close">Cancel</AppButton>
          <AppButton variant="primary" type="submit" :loading="busy">Save changes</AppButton>
        </div>
      </form>
    </AppDialog>

    <AppConfirmDialog
      :open="dialog === 'delete' && !!target"
      title="Delete user"
      confirm-label="Delete user"
      :type-to-confirm="target?.username ?? ''"
      :busy="busy"
      @confirm="submitDelete"
      @close="close"
    >
      <p>
        Delete <strong class="font-semibold text-ink">{{ target?.username }}</strong
        >? Their sessions and site grants are revoked immediately. This cannot be undone.
      </p>
      <AppAlert v-if="dialogError" tone="danger" class="mt-3">{{ dialogError }}</AppAlert>
    </AppConfirmDialog>
  </section>
</template>
