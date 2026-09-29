<script setup lang="ts">
// To review: requests routed to me (department manager, or HR administrator
// when no manager is responsible), pending or awaiting signing.
import { onMounted } from 'vue'
import { UiPage } from '@go-tangra/ui'
import { useOrg } from '@/stores/org'
import RequestList from '../requests/list.vue'

const org = useOrg()
onMounted(() => void org.load().catch(() => {}))
</script>

<template>
  <UiPage title="To review" subtitle="Leave requests waiting for your decision">
    <RequestList view="review" show-person />
  </UiPage>
</template>
