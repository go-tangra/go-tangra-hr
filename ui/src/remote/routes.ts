import type { RouteRecordRaw } from 'vue-router'
import '@/main.css'

const meta = { module: 'hr' }

// Routes mounted by the platform shell under their own error boundary.
export const routes: RouteRecordRaw[] = [
  { path: '/hr', name: 'hr-calendar', component: () => import('@/views/calendar/index.vue'), meta },
  { path: '/hr/requests', name: 'hr-requests', component: () => import('@/views/requests/index.vue'), meta },
  { path: '/hr/requests/:id', name: 'hr-request', component: () => import('@/views/requests/detail.vue'), meta },
  { path: '/hr/review', name: 'hr-review', component: () => import('@/views/review/index.vue'), meta },
  { path: '/hr/allowances', name: 'hr-allowances', component: () => import('@/views/allowances/index.vue'), meta },
  { path: '/hr/absence-types', name: 'hr-absence-types', component: () => import('@/views/absence-types/index.vue'), meta },
  { path: '/hr/departments', name: 'hr-departments', component: () => import('@/views/departments/index.vue'), meta },
  { path: '/hr/holidays', name: 'hr-holidays', component: () => import('@/views/holidays/index.vue'), meta },
  { path: '/hr/stats', name: 'hr-stats', component: () => import('@/views/stats/index.vue'), meta },
]
export default routes
