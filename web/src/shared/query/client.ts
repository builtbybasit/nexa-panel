import { QueryClient } from '@tanstack/vue-query'

/** The one cache owned by the application and reset at every identity boundary. */
export const appQueryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
})
