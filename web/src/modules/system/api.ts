export interface MemorySnapshot {
  supported: boolean
  totalBytes: number
  availableBytes: number
  swapTotalBytes: number
  swapFreeBytes: number
  usedPercent: number
  profile: 'unsupported' | 'compact' | 'standard' | 'pro'
}

export interface PodmanStatus {
  available: boolean
  version?: string
  path?: string
}

export interface SystemOverview {
  observedAt: string
  memory?: MemorySnapshot
  podman: PodmanStatus
  warnings: string[]
}

export async function getSystemOverview(): Promise<SystemOverview> {
  const response = await fetch('/api/v1/system/overview', {
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) {
    throw new Error(`System overview request failed with status ${response.status}`)
  }
  return (await response.json()) as SystemOverview
}
