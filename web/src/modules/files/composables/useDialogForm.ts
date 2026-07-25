import { ref, watch, type Ref } from 'vue'

/**
 * Busy/error plumbing shared by the file manager's form dialogs: one submission
 * at a time, the failure surfaced in the dialog rather than as a toast the user
 * would read after it closed, and both cleared whenever the dialog reopens.
 */
export function useDialogForm(open: Ref<boolean>, onDone: () => void) {
  const busy = ref(false)
  const error = ref('')

  watch(open, (isOpen) => {
    if (isOpen) error.value = ''
  })

  async function submit(action: () => Promise<void>) {
    if (busy.value) return
    busy.value = true
    error.value = ''
    try {
      await action()
      onDone()
    } catch (caught) {
      error.value = caught instanceof Error ? caught.message : 'The file operation failed.'
    } finally {
      busy.value = false
    }
  }

  return { busy, error, submit }
}
