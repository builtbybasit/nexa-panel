import { onScopeDispose, ref } from 'vue'

import { subscribeToJob, type JobEvent } from '@/modules/jobs/api'

export interface FollowOptions {
  /** Runs after the job reaches a terminal state, before success/failure hooks. */
  onSettled?: (event: JobEvent) => void | Promise<void>
  onSuccess?: (event: JobEvent) => void | Promise<void>
  /** Error shown when the job ends in the failed state. */
  failureMessage?: string
}

/**
 * Shared lifecycle for the plan/apply workflows: queue an operation, follow its
 * durable job over SSE, and surface busy/progress/error state to the view.
 * Replaces the watch-job state machine previously copy-pasted into every module.
 */
export function useJobRunner() {
  const busy = ref(false)
  const error = ref('')
  const progress = ref<JobEvent>()
  let stopWatching: (() => void) | undefined

  function stop() {
    stopWatching?.()
    stopWatching = undefined
  }

  function follow(jobId: number, options: FollowOptions = {}) {
    stop()
    progress.value = undefined
    stopWatching = subscribeToJob(
      jobId,
      (event) => {
        progress.value = event
        if (event.state !== 'succeeded' && event.state !== 'failed') return
        stop()
        void (async () => {
          await options.onSettled?.(event)
          if (event.state === 'failed') {
            error.value = options.failureMessage ?? 'The operation failed. Inspect Jobs for the durable failure record.'
          } else {
            await options.onSuccess?.(event)
          }
          busy.value = false
        })()
      },
      () => {
        error.value = 'The live progress stream disconnected. Inspect Jobs for the final state.'
        busy.value = false
      },
    )
  }

  /**
   * Queue an operation and follow the returned job. `action` performs the API
   * call and returns the job id to follow (or undefined to finish immediately).
   */
  async function run(action: () => Promise<number | undefined>, options: FollowOptions = {}) {
    busy.value = true
    error.value = ''
    progress.value = undefined
    try {
      const jobId = await action()
      if (jobId === undefined) {
        busy.value = false
        return
      }
      follow(jobId, options)
    } catch (caught) {
      error.value = caught instanceof Error ? caught.message : 'The operation could not be queued.'
      busy.value = false
    }
  }

  onScopeDispose(stop)

  return { busy, error, progress, run, follow, stop }
}
