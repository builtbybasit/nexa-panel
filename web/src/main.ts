import { VueQueryPlugin } from '@tanstack/vue-query'
import { createPinia } from 'pinia'
import { createApp } from 'vue'

import App from './app/App.vue'
import { router } from './app/router'
import { useIdentityStore } from './modules/identity/store'
import { registerUnauthorizedHandler } from './shared/api/unauthorized'
import { appQueryClient } from './shared/query/client'
import './styles/main.css'

const pinia = createPinia()
const app = createApp(App)

app.use(pinia)
registerUnauthorizedHandler(() => useIdentityStore(pinia).expireSession())
app.use(VueQueryPlugin, { queryClient: appQueryClient }).use(router).mount('#app')
