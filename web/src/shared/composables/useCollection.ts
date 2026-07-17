import { computed, ref, watch } from 'vue'

export interface CollectionOptions<T> {
  /** Text the case-insensitive search matches against for each item. */
  searchText: (item: T) => string
  pageSize?: number
}

/**
 * Client-side search + pagination over an already filtered/sorted list.
 * Views apply their own filter and sort computeds before passing `source`,
 * then render the pager themselves from `page`/`pageCount`.
 */
export function useCollection<T>(source: () => T[], opts: CollectionOptions<T>) {
  const pageSize = opts.pageSize ?? 25
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
  const pageCount = computed(() => Math.max(1, Math.ceil(matching.value / pageSize)))

  watch([search, total], () => {
    page.value = 1
  })
  watch(pageCount, (count) => {
    if (page.value > count) page.value = count
  })

  const items = computed(() => {
    const current = Math.min(Math.max(page.value, 1), pageCount.value)
    const start = (current - 1) * pageSize
    return searched.value.slice(start, start + pageSize)
  })

  return { search, page, pageCount, items, total, matching }
}
