<script setup lang="ts">
import { RouterLink } from 'vue-router'

import { EmptyState } from '@/shared/ui'
</script>

<template>
  <EmptyState
    icon="lock"
    title="You do not have access to this page"
    description="Your account is signed in, but its role does not include the permission required by this page."
  >
    <template #action>
      <RouterLink
        to="/"
        class="inline-flex h-10 items-center justify-center rounded-lg bg-accent-500 px-4 text-sm font-semibold text-accent-950 shadow-[0_8px_24px_-8px] shadow-accent-500/50 transition-colors hover:bg-accent-400"
      >
        Return to overview
      </RouterLink>
    </template>
  </EmptyState>
</template>
