<script setup lang="ts">
import { RouterLink } from 'vue-router'

import AppIcon from './AppIcon.vue'

defineProps<{
  title: string
  eyebrow?: string
  description?: string
  breadcrumbs?: { label: string; to?: string }[]
}>()
</script>

<template>
  <div class="flex flex-wrap items-end justify-between gap-4">
    <div class="min-w-0">
      <nav v-if="breadcrumbs && breadcrumbs.length" aria-label="Breadcrumb" class="mb-1.5">
        <ol class="flex flex-wrap items-center gap-1 text-xs text-ink-muted">
          <li v-for="(crumb, index) in breadcrumbs" :key="`${crumb.label}-${index}`" class="flex items-center gap-1">
            <RouterLink v-if="crumb.to" :to="crumb.to" class="transition-colors hover:text-ink">
              {{ crumb.label }}
            </RouterLink>
            <span v-else>{{ crumb.label }}</span>
            <AppIcon v-if="index < breadcrumbs.length - 1" name="chevron-right" :size="12" />
          </li>
        </ol>
      </nav>
      <p v-if="eyebrow" class="text-[11px] font-bold tracking-[0.14em] text-accent-400 uppercase">{{ eyebrow }}</p>
      <!-- The sole page title since the top bar carries no heading; keep it the h1. -->
      <h1 class="mt-1 text-xl font-semibold tracking-tight text-ink sm:text-2xl">{{ title }}</h1>
      <p v-if="description" class="mt-1.5 max-w-2xl text-sm leading-relaxed text-ink-secondary">{{ description }}</p>
    </div>
    <div v-if="$slots.default" class="flex shrink-0 flex-wrap items-center gap-2">
      <slot />
    </div>
  </div>
</template>
