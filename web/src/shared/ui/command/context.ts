import type { Ref } from 'vue'

import { createContext } from 'reka-ui'

/**
 * Command menu context. `Command` (a reka `ListboxRoot`) owns the filtering:
 * items register their searchable text into `allItems`, groups track their
 * members in `allGroups`, and `filterState` holds the query plus which items and
 * groups currently match.
 */
interface CommandContext {
  allItems: Ref<Map<string, string>>
  allGroups: Ref<Map<string, Set<string>>>
  filterState: {
    search: string
    filtered: { count: number; items: Map<string, number>; groups: Set<string> }
  }
}

export const [useCommand, provideCommandContext] = createContext<CommandContext>('Command')

interface CommandGroupContext {
  id?: string
}

export const [useCommandGroup, provideCommandGroupContext] = createContext<CommandGroupContext>('CommandGroup')
