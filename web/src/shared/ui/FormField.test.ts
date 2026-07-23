// @vitest-environment happy-dom

import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { describe, expect, it } from 'vitest'

import FormField from './FormField.vue'
import { Select, SelectTrigger } from './select'

const SelectField = defineComponent({
  components: { FormField, Select, SelectTrigger },
  template: '<Select><FormField label="Protocol"><SelectTrigger /></FormField></Select>',
})

describe('FormField', () => {
  it('gives a custom trigger an explicit accessible label', () => {
    const wrapper = mount(SelectField)
    const trigger = wrapper.get('button')
    const labelId = trigger.attributes('aria-labelledby')

    expect(labelId).toBeTruthy()
    expect(wrapper.get(`#${labelId}`).text()).toBe('Protocol')
  })
})
