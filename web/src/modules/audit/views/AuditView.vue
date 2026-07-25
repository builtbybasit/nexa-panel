<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'

import { listUsers } from '@/modules/identity/api'
import { formatJobKind } from '@/shared/formatters'
import { useCollection } from '@/shared/composables/useCollection'
import {
  AppAlert,
  AppButton,
  AppCard,
  AppIcon,
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
  EmptyState,
  ListToolbar,
  PageHeader,
  SkeletonRow,
} from '@/shared/ui'

import { listAuditEvents, type AuditEvent } from '../api'

const auditQuery = useQuery({ queryKey: ['audit'], queryFn: listAuditEvents, refetchInterval: 10_000 })
const events = computed(() => auditQuery.data.value ?? [])

// Actor UUID → username. Viewers without users.manage get a 403 here, so the
// lookup never retries and rows silently fall back to the truncated UUID.
const usersQuery = useQuery({ queryKey: ['users'], queryFn: listUsers, retry: false })
const usernameById = computed(() => {
  const map = new Map<string, string>()
  for (const user of usersQuery.data.value ?? []) map.set(user.id, user.username)
  return map
})

function actorLabel(event: AuditEvent): string {
  if (!event.actorUserId) return 'Unauthenticated event'
  return usernameById.value.get(event.actorUserId) ?? `Actor ${event.actorUserId.slice(0, 8)}…`
}

// --- Severity per action pattern, reusing the StatusPill dot palette ---

type Severity = 'danger' | 'warning' | 'neutral'

function severity(action: string): Severity {
  if (/delete|revoke|remove|fail/.test(action)) return 'danger'
  if (/rotate|role/.test(action)) return 'warning'
  return 'neutral'
}

const bulletClasses: Record<Severity, string> = {
  danger: 'bg-rose-300',
  warning: 'bg-amber-300',
  neutral: 'bg-ink-secondary',
}

// --- Client-side filter over actor/action/subject ---

const collection = useCollection(() => events.value, {
  searchText: (event) => `${actorLabel(event)} ${event.actorUserId ?? ''} ${event.action} ${event.subject}`,
  pageSize: 100,
})

function metadataEntries(event: AuditEvent): [string, string][] {
  return Object.entries(event.metadata ?? {}).map(([key, value]) => [
    key,
    typeof value === 'string' ? value : JSON.stringify(value),
  ])
}

const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Security history"
      title="Audit log"
      description="Sign-ins and operation records, append-only and with secrets redacted."
    >
      <AppButton icon="refresh-cw" :loading="auditQuery.isFetching.value" @click="auditQuery.refetch()">Refresh</AppButton>
    </PageHeader>

    <AppCard v-if="auditQuery.isPending.value" flush>
      <div class="divide-y divide-outline px-4 py-1 sm:px-5">
        <SkeletonRow v-for="n in 3" :key="n" />
      </div>
    </AppCard>

    <AppAlert v-else-if="auditQuery.isError.value" tone="danger">
      <p>The audit log couldn't be loaded, or your role can't access it.</p>
      <AppButton size="sm" class="mt-2" @click="auditQuery.refetch()">Retry</AppButton>
    </AppAlert>

    <template v-else>
      <div class="space-y-2">
        <ListToolbar
          :search="collection.search.value"
          :count="collection.matching.value"
          count-label="events"
          placeholder="Filter by actor, action, or subject"
          @update:search="collection.search.value = $event"
        />
        <p class="text-xs text-ink-muted">Showing the latest 100 events.</p>
      </div>

      <AppCard flush>
        <ol v-if="collection.items.value.length" class="divide-y divide-outline px-4 py-1 sm:px-5">
          <li v-for="event in collection.items.value" :key="event.id" class="py-3.5">
            <Collapsible :default-open="false">
              <div class="flex items-start gap-3">
                <CollapsibleTrigger as-child>
                  <button
                    type="button"
                    class="group grid size-7 shrink-0 place-items-center rounded-lg text-ink-muted transition-colors hover:bg-white/[0.05] hover:text-ink"
                    :aria-label="`Show details for event ${event.id}`"
                  >
                    <AppIcon name="chevron-down" :size="15" class="transition-transform group-data-[state=open]:rotate-180" />
                  </button>
                </CollapsibleTrigger>
                <span class="mt-2 size-2 shrink-0 rounded-full" :class="bulletClasses[severity(event.action)]" aria-hidden="true" />
                <div class="min-w-0 flex-1">
                  <strong class="block text-[13px] font-semibold text-ink">{{ formatJobKind(event.action) }}</strong>
                  <small class="block truncate text-xs text-ink-muted">{{ event.subject }}</small>
                </div>
                <div class="hidden min-w-0 text-right sm:block">
                  <span class="block text-xs text-ink-secondary">{{ event.remoteAddress || 'Local control panel' }}</span>
                  <small class="block text-[11px] text-ink-muted">{{ actorLabel(event) }}</small>
                </div>
                <time :datetime="event.occurredAt" class="shrink-0 pt-0.5 font-mono text-[11px] whitespace-nowrap text-ink-muted">
                  {{ timeFormat.format(new Date(event.occurredAt)) }}
                </time>
              </div>

              <CollapsibleContent>
                <div class="mt-3 space-y-3 pl-12">
                  <div>
                    <p class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Subject</p>
                    <p class="mt-1 font-mono text-xs break-all text-ink-secondary">{{ event.subject }}</p>
                  </div>
                  <div v-if="event.actorUserId">
                    <p class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Actor</p>
                    <p class="mt-1 font-mono text-xs break-all text-ink-secondary">
                      {{ actorLabel(event) }} · {{ event.actorUserId }}
                    </p>
                  </div>
                  <div v-if="metadataEntries(event).length">
                    <p class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">Metadata</p>
                    <dl class="mt-1 space-y-1">
                      <div v-for="[key, value] in metadataEntries(event)" :key="key" class="flex gap-2 font-mono text-xs">
                        <dt class="shrink-0 text-ink-muted">{{ key }}:</dt>
                        <dd class="min-w-0 break-all text-ink-secondary">{{ value }}</dd>
                      </div>
                    </dl>
                  </div>
                </div>
              </CollapsibleContent>
            </Collapsible>
          </li>
        </ol>
        <EmptyState
          v-else-if="events.length"
          icon="filter"
          title="No events match"
          description="Clear the filter to see the rest of the latest 100 events."
          class="m-5"
        />
        <EmptyState
          v-else
          icon="file-text"
          title="No audit events"
          description="Sign-ins and operation records will appear here."
          class="m-5"
        />
      </AppCard>
    </template>
  </section>
</template>
