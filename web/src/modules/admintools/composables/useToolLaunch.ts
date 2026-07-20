import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'

import { useIdentityStore } from '@/modules/identity/store'

import { launchTool, listTools, type ToolKind } from '../api'

type SourceEngine = 'mysql' | 'postgresql'
export type ToolAvailability = 'loading' | 'error' | 'ready' | 'inactive'

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
  const identity = useIdentityStore()
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
    return availability(kind) === 'ready'
  }

  function availability(kind: ToolKind): ToolAvailability {
    if (toolsQuery.isPending.value) return 'loading'
    if (toolsQuery.isError.value) return 'error'
    return activeKinds.value.has(kind) ? 'ready' : 'inactive'
  }

  async function launch(kind: ToolKind, sourceEngine: SourceEngine, databaseId: string) {
    if (!identity.can('operations.apply')) {
      error.value = 'Only an administrator can launch a database web client.'
      return
    }
    if (!isReady(kind)) {
      error.value = availability(kind) === 'error' ? 'The admin-tool status could not be loaded.' : 'Install the web client before launching it.'
      return
    }

    // Reserve the tab synchronously while the click still has transient user
    // activation. Authorization (and possible MFA step-up) can finish later.
    const opened = window.open('about:blank', '_blank')
    if (opened) opened.opener = null

    launchingId.value = databaseId
    error.value = ''
    blocked.value = undefined
    try {
      const { url } = await launchTool(kind, { sourceEngine, databaseId, accountId: '' })
      if (opened) opened.location.replace(url)
      else blocked.value = { url }
    } catch (caught) {
      opened?.close()
      error.value = caught instanceof Error ? caught.message : 'The database web client could not be opened.'
    } finally {
      launchingId.value = undefined
    }
  }

  return { toolsQuery, activeKinds, availability, isReady, launch, launchingId, error, blocked }
}
