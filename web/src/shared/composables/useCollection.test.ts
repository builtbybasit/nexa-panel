import { describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'

import { useCollection } from './useCollection'

// `page` is corrected by watchers, which flush before a component would
// re-render but not synchronously with the assignment that triggered them.

interface Row {
  name: string
}

function rows(count: number): Row[] {
  return Array.from({ length: count }, (_, index) => ({ name: `row-${index + 1}` }))
}

describe('useCollection', () => {
  it('pages the source list and reports the shown range', () => {
    const collection = useCollection(() => rows(12), { searchText: (row) => row.name, pageSize: 5 })

    expect(collection.pageCount.value).toBe(3)
    expect(collection.items.value).toHaveLength(5)
    expect(collection.rangeStart.value).toBe(1)
    expect(collection.rangeEnd.value).toBe(5)

    collection.page.value = 3
    // The last page is short, so the range must stop at the real total rather
    // than at page * pageSize.
    expect(collection.items.value).toHaveLength(2)
    expect(collection.rangeStart.value).toBe(11)
    expect(collection.rangeEnd.value).toBe(12)
  })

  it('reports an empty range when nothing matches', () => {
    const collection = useCollection(() => rows(3), { searchText: (row) => row.name })
    collection.search.value = 'nothing'

    expect(collection.matching.value).toBe(0)
    expect(collection.rangeStart.value).toBe(0)
    expect(collection.rangeEnd.value).toBe(0)
  })

  it('repages when the page size changes', () => {
    const collection = useCollection(() => rows(12), { searchText: (row) => row.name, pageSize: 5 })

    collection.pageSize.value = 10
    expect(collection.pageCount.value).toBe(2)
    expect(collection.items.value).toHaveLength(10)
  })

  // Growing the page size can leave `page` past the end; without clamping the
  // pager would sit on a page that renders nothing.
  it('clamps the current page when a larger page size shrinks the list', async () => {
    const collection = useCollection(() => rows(12), { searchText: (row) => row.name, pageSize: 5 })
    collection.page.value = 3

    collection.pageSize.value = 10
    await nextTick()

    expect(collection.page.value).toBe(2)
    expect(collection.items.value).toHaveLength(2)
    expect(collection.rangeStart.value).toBe(11)
  })

  it('returns to the first page when the search changes', async () => {
    const collection = useCollection(() => rows(12), { searchText: (row) => row.name, pageSize: 5 })
    collection.page.value = 3

    collection.search.value = 'row-1'
    await nextTick()

    expect(collection.page.value).toBe(1)
    // row-1, row-10, row-11, row-12
    expect(collection.matching.value).toBe(4)
  })

  it('tracks a reactive source', () => {
    const source = ref(rows(2))
    const collection = useCollection(() => source.value, { searchText: (row) => row.name })

    expect(collection.total.value).toBe(2)
    source.value = rows(7)
    expect(collection.total.value).toBe(7)
    expect(collection.rangeEnd.value).toBe(7)
  })
})
