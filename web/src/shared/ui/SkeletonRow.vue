<template>
  <!-- Shimmer placeholder matching ResourceRow geometry (avatar + title/subtitle). -->
  <div class="flex w-full items-center gap-3 rounded-xl border border-transparent px-3 py-2.5" aria-hidden="true">
    <span class="size-9 shrink-0 animate-pulse rounded-lg bg-white/[0.06]" />
    <span class="min-w-0 flex-1 space-y-2">
      <span class="block h-3.5 w-2/5 max-w-40 animate-pulse rounded bg-white/[0.06]" />
      <span class="block h-3 w-1/4 max-w-24 animate-pulse rounded bg-white/[0.04]" />
    </span>
  </div>
</template>
