<script setup lang="ts">
import { RouterLink } from 'vue-router'

import { AppIcon, EmptyState } from '@/shared/ui'
</script>

<template>
  <div class="mx-auto max-w-lg pt-16">
    <EmptyState
      icon="search"
      title="Page not found"
      description="This address does not match anything in the panel. It may have been moved or removed."
    >
      <template #action>
        <!-- A link styled as a button: nesting a real button inside an anchor is invalid. -->
        <RouterLink
          to="/"
          class="inline-flex h-10 items-center justify-center gap-2 rounded-lg bg-accent-500 px-4 text-sm font-semibold text-accent-950 shadow-[0_8px_24px_-8px] shadow-accent-500/50 transition-colors hover:bg-accent-400"
        >
          <AppIcon name="arrow-left" :size="16" />
          Back to overview
        </RouterLink>
      </template>
    </EmptyState>
  </div>
</template>
