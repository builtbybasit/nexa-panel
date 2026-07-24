import type { JobMessage, useJobRunner } from '@/shared/composables/useJobRunner'

type Runner = ReturnType<typeof useJobRunner>

/** Props for JobFailureNotice, linking to the job when one actually started. */
export function failureProps(runner: Runner): { message: string; jobId?: number } {
  const props: { message: string; jobId?: number } = { message: runner.error.value }
  if (runner.progress.value && runner.jobId.value !== undefined) props.jobId = runner.jobId.value
  return props
}

/** Props for JobProgress: the live message stream plus its start time. */
export function progressProps(runner: Runner): { messages: JobMessage[]; startedAtMs?: number } {
  const props: { messages: JobMessage[]; startedAtMs?: number } = { messages: runner.messages.value }
  if (runner.startedAtMs.value !== undefined) props.startedAtMs = runner.startedAtMs.value
  return props
}
