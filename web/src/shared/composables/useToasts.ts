import { ref } from 'vue'

interface ToastInput {
  title: string
  body?: string
  tone?: 'success' | 'warning' | 'danger' | 'info'
  /** Route the optional action link navigates to. */
  to?: string
  toLabel?: string
  timeoutMs?: number
}

interface Toast {
  id: number
  title: string
  body?: string
  tone: 'success' | 'warning' | 'danger' | 'info'
  to?: string
  toLabel?: string
  timeoutMs: number
}

const DEFAULT_TIMEOUT_MS = 6000
const DANGER_TIMEOUT_MS = 10000
/** Floor applied when resuming a paused toast so it never vanishes on mouse-out. */
const RESUME_MIN_MS = 1000

const toasts = ref<Toast[]>([])
let nextId = 1
let paused = false

const timers = new Map<number, { handle: ReturnType<typeof setTimeout>; endsAt: number }>()
const pausedRemainingMs = new Map<number, number>()

function arm(id: number, ms: number) {
  timers.set(id, { handle: setTimeout(() => dismiss(id), ms), endsAt: Date.now() + ms })
}

function push(input: ToastInput): number {
  const tone = input.tone ?? 'info'
  const toast: Toast = {
    ...input,
    id: nextId++,
    tone,
    timeoutMs: input.timeoutMs ?? (tone === 'danger' ? DANGER_TIMEOUT_MS : DEFAULT_TIMEOUT_MS),
  }
  toasts.value = [...toasts.value, toast]
  if (paused) pausedRemainingMs.set(toast.id, toast.timeoutMs)
  else arm(toast.id, toast.timeoutMs)
  return toast.id
}

function dismiss(id: number) {
  const timer = timers.get(id)
  if (timer) clearTimeout(timer.handle)
  timers.delete(id)
  pausedRemainingMs.delete(id)
  toasts.value = toasts.value.filter((toast) => toast.id !== id)
}

/** Freezes every auto-dismiss countdown. Called by ToastHost while hovered/focused. */
export function pauseToastTimers() {
  if (paused) return
  paused = true
  for (const [id, timer] of timers) {
    clearTimeout(timer.handle)
    pausedRemainingMs.set(id, Math.max(timer.endsAt - Date.now(), RESUME_MIN_MS))
  }
  timers.clear()
}

/** Restarts the countdowns with the time each toast had left when paused. */
export function resumeToastTimers() {
  if (!paused) return
  paused = false
  for (const toast of toasts.value) {
    arm(toast.id, pausedRemainingMs.get(toast.id) ?? toast.timeoutMs)
  }
  pausedRemainingMs.clear()
}

/**
 * Module-level toast store: any component can push a notification and the
 * single ToastHost mounted in App.vue renders the stack.
 */
export function useToasts() {
  return { toasts, push, dismiss }
}
