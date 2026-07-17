<template>
  <!-- Shimmer placeholder matching MetricCard geometry (label/icon, value, detail). -->
  <article class="rounded-2xl border border-outline bg-surface/80 p-5" aria-hidden="true">
    <div class="flex items-center justify-between gap-3">
      <span class="h-3 w-20 animate-pulse rounded bg-white/[0.06]" />
      <span class="size-8 shrink-0 animate-pulse rounded-lg bg-white/[0.06]" />
    </div>
    <span class="mt-2.5 block h-7 w-24 animate-pulse rounded bg-white/[0.06]" />
    <span class="mt-2 block h-3.5 w-32 animate-pulse rounded bg-white/[0.04]" />
  </article>
</template>
