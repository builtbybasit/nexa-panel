import { ref, type Ref } from 'vue'

import { useJobRunner } from '@/shared/composables/useJobRunner'

import { getJob } from '../../jobs/api'
import { archiveEntries, directorySize, extractEntry, type DirectorySizeResult } from '../api'

/**
 * The three file operations too slow for a request: packing, extracting and
 * walking a tree for its size. Each queues a durable job and follows it, so the
 * view only supplies the paths and gets progress and results back.
 */
export function useFileJobs(siteId: Ref<string>, onChanged: () => Promise<void> | void) {
  const runner = useJobRunner()
  const sizeResult = ref<DirectorySizeResult & { path: string }>()

  function archive(paths: string[], target: string, onSuccess?: () => void) {
    if (!paths.length) return
    void runner.run(async () => (await archiveEntries(siteId.value, { paths, target })).job.id, {
      onSuccess: async () => {
        onSuccess?.()
        await onChanged()
      },
      failureMessage: 'Archive failed',
      successToast: 'Archive created',
    })
  }

  function extract(path: string, targetDir: string) {
    void runner.run(async () => (await extractEntry(siteId.value, { path, targetDir })).job.id, {
      onSuccess: onChanged,
      failureMessage: 'Extract failed',
      successToast: 'Archive extracted',
    })
  }

  function computeSize(path: string) {
    void runner.run(async () => (await directorySize(siteId.value, path)).job.id, {
      // The totals ride on the job record rather than the completion event.
      onSuccess: async (event) => {
        const result = ((await getJob(event.jobId)).result ?? {}) as Partial<DirectorySizeResult>
        sizeResult.value = {
          path,
          bytes: Number(result.bytes ?? 0),
          files: Number(result.files ?? 0),
          dirs: Number(result.dirs ?? 0),
          truncated: Boolean(result.truncated),
        }
      },
      failureMessage: 'Could not compute the folder size',
    })
  }

  return { runner, sizeResult, archive, extract, computeSize }
}
