import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { launchTool, listTools, type ToolKind } from '../api'

type SourceEngine = 'mysql' | 'postgresql'

/**
 * Drives the one-click "open the database web client" launch from the MySQL and
 * PostgreSQL database tables. The account is left blank so the gateway logs in
 * as the database's owner — the row already identifies the database, and its
 * owner is the natural identity, exactly like FastPanel's per-row launch icon.
 *
 * The tool must be deployed (active) before it can be launched; `isReady`
 * reflects the admin-tools deployment state so callers can guide the user to
 * install it from the Applications page when it is not.
 */
export function useToolLaunch() {
  const toolsQuery = useQuery({ queryKey: ['admin-tools'], queryFn: listTools, retry: false })
  const activeKinds = computed(
    () => new Set((toolsQuery.data.value ?? []).filter((tool) => tool.status === 'active').map((tool) => tool.kind)),
  )

  // Keyed by database id so only the clicked row shows a spinner.
  const launchingId = ref<string>()
  const error = ref('')
  // window.open returns null when the browser blocks the tab; surface a manual link.
  const blocked = ref<{ url: string }>()

  function isReady(kind: ToolKind) {
    return activeKinds.value.has(kind)
  }

  async function launch(kind: ToolKind, sourceEngine: SourceEngine, databaseId: string) {
    launchingId.value = databaseId
    error.value = ''
    blocked.value = undefined
    try {
      const { url } = await launchTool(kind, { sourceEngine, databaseId, accountId: '' })
      // window.open returns null when the feature string contains `noopener`,
      // so sever the opener by hand to keep popup-blocker detection working.
      const opened = window.open(url, '_blank')
      if (opened) opened.opener = null
      else blocked.value = { url }
    } catch (caught) {
      error.value = caught instanceof Error ? caught.message : 'The database web client could not be opened.'
    } finally {
      launchingId.value = undefined
    }
  }

  return { toolsQuery, activeKinds, isReady, launch, launchingId, error, blocked }
}
