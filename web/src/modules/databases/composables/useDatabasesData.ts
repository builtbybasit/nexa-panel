import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'

import {
  listDatabases,
  listServers,
  listUsers,
  type DatabaseServer,
  type DatabaseUser,
} from '../api'
import { serverLabel, serverShort, userLabel } from '../lib/engines'

/**
 * The queries and lookups every databases view shares. One composable instead
 * of per-view copies keeps the label logic and refresh fan-out identical
 * across the list, create, and detail pages.
 */
export function useDatabasesData() {
  const serversQuery = useQuery({ queryKey: ['database-servers'], queryFn: listServers, retry: false })
  const usersQuery = useQuery({ queryKey: ['database-users'], queryFn: listUsers, retry: false })
  const databasesQuery = useQuery({ queryKey: ['managed-databases'], queryFn: listDatabases, retry: false })

  const servers = computed(() => serversQuery.data.value ?? [])
  const users = computed(() => usersQuery.data.value ?? [])
  const databases = computed(() => databasesQuery.data.value ?? [])
  const activeServers = computed(() =>
    servers.value.filter((item) => item.status === 'active' || item.status === 'online'),
  )
  const activeUsers = computed(() => users.value.filter((item) => item.status === 'active'))

  function server(id: string): DatabaseServer | undefined {
    return servers.value.find((item) => item.id === id)
  }

  function user(id: string): DatabaseUser | undefined {
    return users.value.find((item) => item.id === id)
  }

  function serverName(id: string): string {
    const item = server(id)
    return item ? serverLabel(item) : id
  }

  function serverNameShort(id: string): string {
    const item = server(id)
    return item ? serverShort(item) : id
  }

  function userName(id: string): string {
    const item = user(id)
    return item ? userLabel(item) : id
  }

  async function refreshAll() {
    await Promise.all([serversQuery.refetch(), usersQuery.refetch(), databasesQuery.refetch()])
  }

  return {
    serversQuery,
    usersQuery,
    databasesQuery,
    servers,
    users,
    databases,
    activeServers,
    activeUsers,
    server,
    user,
    serverName,
    serverNameShort,
    userName,
    refreshAll,
  }
}
