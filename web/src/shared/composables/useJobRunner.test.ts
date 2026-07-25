import { describe, expect, it } from 'vitest'

import { useJobRunner } from './useJobRunner'

describe('useJobRunner adapters', () => {
  describe('failureProps', () => {
    it('carries the error message and omits jobId when no job exists', () => {
      const runner = useJobRunner()
      runner.error.value = 'Could not queue'

      const props = runner.failureProps.value
      expect(props.message).toBe('Could not queue')
      // exactOptionalPropertyTypes: the key must be absent, not `undefined`.
      expect('jobId' in props).toBe(false)
    })

    it('links to the job whenever one was queued, regardless of progress state', () => {
      const runner = useJobRunner()
      runner.error.value = 'The live progress stream disconnected.'
      runner.jobId.value = 42
      // No progress event yet (a disconnect before the first event): the older
      // per-view predicates gated on progress.state and would drop the link;
      // the shared adapter links on jobId alone so the job stays reachable.
      expect(runner.progress.value).toBeUndefined()

      expect(runner.failureProps.value).toEqual({ message: 'The live progress stream disconnected.', jobId: 42 })
    })
  })

  describe('progressProps', () => {
    it('carries messages and omits startedAtMs until a run begins', () => {
      const runner = useJobRunner()
      runner.messages.value = [{ text: 'Queued', at: 1 }]

      const props = runner.progressProps.value
      expect(props.messages).toEqual([{ text: 'Queued', at: 1 }])
      expect('startedAtMs' in props).toBe(false)
    })

    it('includes startedAtMs once the run has a start time', () => {
      const runner = useJobRunner()
      runner.messages.value = [{ text: 'Running', at: 2 }]
      runner.startedAtMs.value = 1000

      expect(runner.progressProps.value).toEqual({ messages: [{ text: 'Running', at: 2 }], startedAtMs: 1000 })
    })
  })
})
