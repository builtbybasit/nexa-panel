import { VueQueryPlugin } from '@tanstack/vue-query'
import { createPinia } from 'pinia'
import { createApp } from 'vue'

import App from './app/App.vue'
import { router } from './app/router'
import './styles/main.css'

createApp(App).use(createPinia()).use(VueQueryPlugin).use(router).mount('#app')
