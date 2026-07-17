import { computed, ref, watch } from 'vue'

export interface CollectionOptions<T> {
  /** Text the case-insensitive search matches against for each item. */
  searchText: (item: T) => string
  /** Starting rows per page; `pageSize` in the return value is writable so a pager can change it. */
  pageSize?: number
}

/**
 * Client-side search + pagination over an already filtered/sorted list.
 * Views apply their own filter and sort computeds before passing `source`,
 * then render the pager themselves from `page`/`pageCount`.
 */
export function useCollection<T>(source: () => T[], opts: CollectionOptions<T>) {
  const pageSize = ref(opts.pageSize ?? 25)
  const search = ref('')
  const page = ref(1)

  const all = computed(source)
  const searched = computed(() => {
    const needle = search.value.trim().toLowerCase()
    if (!needle) return all.value
    return all.value.filter((item) => opts.searchText(item).toLowerCase().includes(needle))
  })

  /** Size of the unsearched source list. */
  const total = computed(() => all.value.length)
  /** Items matching the current search, across all pages. */
  const matching = computed(() => searched.value.length)
  const pageCount = computed(() => Math.max(1, Math.ceil(matching.value / pageSize.value)))

  watch([search, total], () => {
    page.value = 1
  })
  // Also covers a page-size change shrinking the list out from under `page`.
  watch(pageCount, (count) => {
    if (page.value > count) page.value = count
  })

  const items = computed(() => {
    const current = Math.min(Math.max(page.value, 1), pageCount.value)
    const start = (current - 1) * pageSize.value
    return searched.value.slice(start, start + pageSize.value)
  })

  /** 1-based index of the first row on the current page; 0 when nothing matches. */
  const rangeStart = computed(() => (matching.value === 0 ? 0 : (page.value - 1) * pageSize.value + 1))
  const rangeEnd = computed(() => Math.min(page.value * pageSize.value, matching.value))

  return { search, page, pageSize, pageCount, items, total, matching, rangeStart, rangeEnd }
}
