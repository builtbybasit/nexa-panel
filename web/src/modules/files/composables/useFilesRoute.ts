import { useQuery } from '@tanstack/vue-query'
import { computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { listSites } from '../../sites/api'

/**
 * Which site and which directory the file manager is looking at. Both live in
 * the route query rather than in component state so deep links restore a
 * location and the browser's back button walks the browsing history.
 */
export function useFilesRoute() {
  const route = useRoute()
  const router = useRouter()

  const sitesQuery = useQuery({ queryKey: ['sites'], queryFn: listSites, retry: false })
  const activeSites = computed(() => (sitesQuery.data.value ?? []).filter((site) => site.status === 'active'))
  const hasAnySites = computed(() => (sitesQuery.data.value ?? []).length > 0)

  const siteId = computed(() => (typeof route.query.site === 'string' ? route.query.site : ''))
  const selectedSite = computed(() => activeSites.value.find((site) => site.id === siteId.value))

  function selectSite(id: string, keepPath = false) {
    const query = { ...route.query }
    if (id) query.site = id
    else delete query.site
    if (!keepPath) delete query.path
    void router.replace({ query })
  }

  const siteSelection = computed<string>({
    get: () => siteId.value,
    set: (value) => selectSite(value),
  })

  watch(
    activeSites,
    (sites) => {
      const first = sites[0]
      if (!siteId.value && first) selectSite(first.id, true)
    },
    { immediate: true },
  )

  const path = computed(() => (typeof route.query.path === 'string' && route.query.path ? route.query.path : '.'))

  function setPath(next: string) {
    if (next === path.value) return
    const query = { ...route.query }
    if (next === '.') delete query.path
    else query.path = next
    void router.push({ query })
  }

  return { sitesQuery, activeSites, hasAnySites, siteId, selectedSite, siteSelection, path, setPath }
}
