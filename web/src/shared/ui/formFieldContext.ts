import type { ComputedRef, InjectionKey } from 'vue'
import { inject } from 'vue'

export interface FormFieldContext {
  labelId: string
  descriptionId: ComputedRef<string | undefined>
}

export const formFieldContextKey: InjectionKey<FormFieldContext> = Symbol('FormField')

export function useFormFieldContext(): FormFieldContext | undefined {
  return inject(formFieldContextKey, undefined)
}
