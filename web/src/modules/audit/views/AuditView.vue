<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'

import { formatJobKind } from '@/shared/formatters'
import { AppAlert, AppButton, AppCard, EmptyState, PageHeader } from '@/shared/ui'

import { listAuditEvents } from '../api'

const auditQuery = useQuery({ queryKey: ['audit'], queryFn: listAuditEvents, refetchInterval: 10_000 })
const events = computed(() => auditQuery.data.value ?? [])

const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
</script>

<template>
  <section class="space-y-6">
    <PageHeader
      eyebrow="Append-only security history"
      title="Audit log"
      description="Identity and operation lifecycle records with secrets redacted."
    >
      <AppButton icon="refresh-cw" :loading="auditQuery.isFetching.value" @click="auditQuery.refetch()">Refresh</AppButton>
    </PageHeader>

    <AppAlert v-if="auditQuery.isPending.value" tone="info">Loading security and operation history…</AppAlert>
    <AppAlert v-else-if="auditQuery.isError.value" tone="danger">
      The audit log is unavailable or your role cannot access it.
    </AppAlert>

    <AppCard v-else flush>
      <ol v-if="events.length" class="divide-y divide-outline px-4 py-1 sm:px-5">
        <li v-for="event in events" :key="event.id" class="flex items-start gap-4 py-3.5">
          <span class="mt-1.5 size-2 shrink-0 rounded-full bg-accent-400/70" aria-hidden="true" />
          <div class="min-w-0 flex-1">
            <strong class="block text-[13px] font-semibold text-ink">{{ formatJobKind(event.action) }}</strong>
            <small class="block truncate text-xs text-ink-muted">{{ event.subject }}</small>
          </div>
          <div class="hidden min-w-0 text-right sm:block">
            <span class="block text-xs text-ink-secondary">{{ event.remoteAddress || 'Local control plane' }}</span>
            <small class="block text-[11px] text-ink-muted">
              {{ event.actorUserId ? `Actor ${event.actorUserId.slice(0, 8)}…` : 'Unauthenticated event' }}
            </small>
          </div>
          <time :datetime="event.occurredAt" class="shrink-0 pt-0.5 font-mono text-[11px] whitespace-nowrap text-ink-muted">
            {{ timeFormat.format(new Date(event.occurredAt)) }}
          </time>
        </li>
      </ol>
      <EmptyState
        v-else
        icon="file-text"
        title="No audit events"
        description="Identity and operation lifecycle records will appear here."
        class="m-5"
      />
    </AppCard>
  </section>
</template>
