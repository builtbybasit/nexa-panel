import { addCollection } from '@iconify/vue'
import { VueQueryPlugin } from '@tanstack/vue-query'
import { createPinia } from 'pinia'
import { createApp } from 'vue'

import { icons as lucideIcons } from '@iconify-json/lucide'

import App from './app/App.vue'
import { router } from './app/router'
import './styles/main.css'

// Register Lucide offline so <AppIcon> renders without any runtime Iconify API.
addCollection(lucideIcons)

createApp(App).use(createPinia()).use(VueQueryPlugin).use(router).mount('#app')
