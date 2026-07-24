<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { AppAlert, AppButton, AppCard, AppIcon, FeatureTile, StatusPill } from '@/shared/ui'
import CopyField from '@/shared/ui/CopyField.vue'

import type { Site } from '../api'

/** Credentials for the database the wizard provisioned; shown exactly once. */
export interface CreatedDatabase {
  id: string
  name: string
  username: string
  /** Only set when the wizard created the user; an existing owner keeps its password. */
  password?: string
  serverName: string
}

/** SFTP credentials staged at creation; the password is shown exactly once. */
export interface StagedSftp {
  username: string
  host: string
  port: number
  password: string
}

const props = defineProps<{
  site: Site
  database?: CreatedDatabase
  /** The site exists but its database provisioning failed. */
  databaseError?: string
  sftp?: StagedSftp
  /** The site exists but its SFTP credentials could not be staged. */
  sftpError?: string
}>()

const emit = defineEmits<{ createAnother: [] }>()

const router = useRouter()
const identity = useIdentityStore()

const reviewLabel = computed(() =>
  identity.can('operations.apply') ? 'Review & activate' : 'Review plan',
)

interface ActionTile {
  label: string
  icon: string
  to: string
  description: string
}

// The launch pad: everything you'd reach for right after creating a site,
// permission-gated so nobody lands on a page they cannot use.
const tiles = computed<ActionTile[]>(() => {
  const id = props.site.id
  const items: ActionTile[] = [
    { label: 'Site dashboard', icon: 'panels-top-left', to: `/sites/${id}`, description: 'Status, plan, and management' },
  ]
  if (identity.can('files.read')) {
    items.push({ label: 'File manager', icon: 'folder', to: `/files?site=${id}`, description: 'Upload your application' })
  }
  if (identity.can('databases.read')) {
    items.push({
      label: 'Database',
      icon: 'database',
      to: props.database ? `/databases/${encodeURIComponent(props.database.id)}` : '/databases',
      description: props.database ? props.database.name : 'Create or connect one',
    })
  }
  if (identity.can('domains.read')) {
    items.push({ label: 'Domains (DNS)', icon: 'globe', to: `/domains?site=${id}`, description: 'Point more hostnames here' })
  }
  if (identity.can('certificates.read')) {
    items.push({ label: 'SSL certificate', icon: 'lock', to: `/certificates?site=${id}&create=1`, description: 'Serve over HTTPS' })
  }
  if (identity.can('operations.apply')) {
    items.push({
      label: 'SFTP access',
      icon: 'server',
      to: `/sftp?site=${id}`,
      description: props.sftp ? 'Ready — live after activation' : 'Enable once the site is live',
    })
  }
  if (identity.can('logs.read')) {
    items.push({ label: 'Logs', icon: 'file-text', to: `/logs?site=${id}`, description: 'Access and error logs' })
  }
  items.push({ label: 'Settings', icon: 'settings-2', to: `/sites/${id}/settings`, description: 'Nginx and PHP-FPM tuning' })
  return items
})
</script>

<template>
  <div class="space-y-4">
    <AppCard>
      <div class="grid gap-8 lg:grid-cols-[1.1fr_1fr] lg:items-center">
        <div>
          <span class="grid size-12 place-items-center rounded-2xl border border-emerald-400/25 bg-emerald-400/10 text-emerald-300">
            <AppIcon name="check" :size="24" />
          </span>
          <h2 class="mt-4 text-xl font-semibold tracking-tight text-ink sm:text-2xl">
            {{ site.primaryDomain }} created!
          </h2>
          <p class="mt-2 max-w-md text-[13px] leading-relaxed text-ink-secondary">
            Your site is configured and a plan is ready. Review it before activation — nothing changes on the server
            until an administrator approves it.
          </p>
          <div class="mt-3">
            <StatusPill :status="site.status" />
          </div>
          <div class="mt-6 flex flex-wrap gap-2">
            <AppButton variant="primary" icon="arrow-right" @click="router.push(`/sites/${site.id}`)">
              {{ reviewLabel }}
            </AppButton>
            <AppButton variant="ghost" @click="emit('createAnother')">Create another site</AppButton>
          </div>
        </div>

        <!-- Decorative browser mock -->
        <div class="hidden overflow-hidden rounded-2xl border border-outline bg-canvas/60 lg:block" aria-hidden="true">
          <div class="flex items-center gap-2 border-b border-outline bg-white/[0.02] px-3 py-2.5">
            <span class="size-2.5 rounded-full bg-white/10" />
            <span class="size-2.5 rounded-full bg-white/10" />
            <span class="size-2.5 rounded-full bg-white/10" />
            <div
              class="ml-2 flex flex-1 items-center gap-1.5 truncate rounded-md border border-outline bg-canvas/80 px-2.5 py-1 font-mono text-[11px] text-accent-200"
            >
              <AppIcon name="lock" :size="11" class="shrink-0 text-ink-muted" />
              {{ site.primaryDomain }}
            </div>
          </div>
          <div class="space-y-3 p-5">
            <div class="h-2.5 w-2/3 rounded-full bg-white/[0.07]" />
            <div class="h-2 w-full rounded-full bg-white/[0.04]" />
            <div class="h-2 w-5/6 rounded-full bg-white/[0.04]" />
            <div class="mt-4 grid grid-cols-3 gap-2">
              <div class="h-12 rounded-lg border border-outline bg-white/[0.02]" />
              <div class="h-12 rounded-lg border border-outline bg-white/[0.02]" />
              <div class="h-12 rounded-lg border border-outline bg-white/[0.02]" />
            </div>
            <div class="mt-2 h-8 w-28 rounded-lg bg-accent-500/20" />
          </div>
        </div>
      </div>
    </AppCard>

    <AppAlert v-if="databaseError" tone="warning">{{ databaseError }}</AppAlert>

    <AppCard
      v-if="database"
      eyebrow="Database"
      :title="`${database.name} on ${database.serverName}`"
      :description="
        database.password
          ? 'Copy the password now — the panel stores it encrypted and never shows it again.'
          : 'Owned by an existing user — its password is unchanged.'
      "
    >
      <div class="grid gap-4 sm:grid-cols-3">
        <CopyField label="Database" :value="database.name" />
        <CopyField label="User" :value="database.username" />
        <CopyField v-if="database.password" label="Password" :value="database.password" />
      </div>
    </AppCard>

    <AppAlert v-if="sftpError" tone="warning">{{ sftpError }}</AppAlert>

    <AppCard
      v-if="sftp"
      eyebrow="SFTP"
      :title="`${sftp.username} on ${sftp.host}`"
      description="Copy the password now — only a hash is stored, and it is never shown again. Logins start the moment the site is activated."
    >
      <div class="grid gap-4 sm:grid-cols-3">
        <CopyField label="Username" :value="sftp.username" />
        <CopyField label="Host · Port" :value="`${sftp.host}:${sftp.port}`" />
        <CopyField label="Password" :value="sftp.password" />
      </div>
    </AppCard>

    <AppCard eyebrow="Next steps" title="Manage your new site">
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        <FeatureTile v-for="tile in tiles" :key="tile.label" v-bind="tile" />
      </div>
    </AppCard>
  </div>
</template>
