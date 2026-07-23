import { onScopeDispose, ref } from 'vue'

import { subscribeToJob, type JobEvent } from '@/modules/jobs/api'
import { useToasts } from '@/shared/composables/useToasts'

/** One progress message observed during a run. */
export interface JobMessage {
  text: string
  at: number
}

const MESSAGE_LIMIT = 200

export interface FollowOptions {
  /** Runs after the job reaches a terminal state, before success/failure hooks. */
  onSettled?: (event: JobEvent) => void | Promise<void>
  onSuccess?: (event: JobEvent) => void | Promise<void>
  /** Error shown (and toast title used) when the job ends in the failed state. */
  failureMessage?: string
  /** When set, a success toast with this title is pushed on completion. */
  successToast?: string
}

/**
 * Shared lifecycle for the plan/apply workflows: queue an operation, follow its
 * durable job over SSE, and surface busy/progress/error state to the view.
 * Replaces the watch-job state machine previously copy-pasted into every module.
 */
export function useJobRunner() {
  const toasts = useToasts()
  const busy = ref(false)
  const error = ref('')
  const progress = ref<JobEvent>()
  const jobId = ref<number>()
  const messages = ref<JobMessage[]>([])
  const startedAtMs = ref<number>()
  const reconnecting = ref(false)
  let stopWatching: (() => void) | undefined

  function stop() {
    stopWatching?.()
    stopWatching = undefined
  }

  function beginRun() {
    // Clear the previous run's job id so a failure to even queue this run
    // never renders a "View job" link pointing at an unrelated job.
    jobId.value = undefined
    messages.value = []
    startedAtMs.value = Date.now()
    reconnecting.value = false
  }

  /** Stops following and clears every piece of run state, including busy. */
  function reset() {
    stop()
    busy.value = false
    error.value = ''
    progress.value = undefined
    jobId.value = undefined
    messages.value = []
    startedAtMs.value = undefined
    reconnecting.value = false
  }

  function record(event: JobEvent) {
    if (!event.message) return
    if (messages.value[messages.value.length - 1]?.text === event.message) return
    if (messages.value.length >= MESSAGE_LIMIT) messages.value.shift()
    messages.value.push({ text: event.message, at: Date.now() })
  }

  function watchJob(id: number, options: FollowOptions = {}) {
    stop()
    progress.value = undefined
    jobId.value = id
    stopWatching = subscribeToJob(
      id,
      (event) => {
        reconnecting.value = false
        progress.value = event
        record(event)
        if (event.state !== 'succeeded' && event.state !== 'failed') return
        stop()
        void (async () => {
          await options.onSettled?.(event)
          if (event.state === 'failed') {
            error.value = options.failureMessage ?? 'The operation failed.'
            toasts.push({
              title: options.failureMessage ?? 'Operation failed',
              tone: 'danger',
              to: `/jobs?job=${event.jobId}`,
              toLabel: 'View job',
            })
          } else {
            if (options.successToast) {
              toasts.push({ title: options.successToast, tone: 'success' })
            }
            await options.onSuccess?.(event)
          }
          busy.value = false
        })()
      },
      () => {
        reconnecting.value = false
        error.value = 'The live progress stream disconnected. Check Jobs for the final state.'
        busy.value = false
      },
      () => {
        reconnecting.value = true
      },
    )
  }

  /** Follow an already-queued job. Starts a fresh message timeline. */
  function follow(id: number, options: FollowOptions = {}) {
    beginRun()
    watchJob(id, options)
  }

  /**
   * Queue an operation and follow the returned job. `action` performs the API
   * call and returns the job id to follow (or undefined to finish immediately).
   */
  async function run(action: () => Promise<number | undefined>, options: FollowOptions = {}) {
    busy.value = true
    error.value = ''
    progress.value = undefined
    beginRun()
    try {
      const id = await action()
      if (id === undefined) {
        busy.value = false
        return
      }
      watchJob(id, options)
    } catch (caught) {
      error.value = caught instanceof Error ? caught.message : 'The operation could not be queued.'
      busy.value = false
    }
  }

  onScopeDispose(stop)

  return { busy, error, progress, jobId, messages, startedAtMs, reconnecting, run, follow, stop, reset }
}
