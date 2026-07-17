import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * Merge conditional class lists and de-dupe conflicting Tailwind utilities,
 * so a component's base classes can be overridden via an incoming `class` prop
 * (the shadcn-style composition pattern used across `shared/ui`).
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
