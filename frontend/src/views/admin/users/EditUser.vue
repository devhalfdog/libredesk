<template>
  <div class="mb-5">
    <CustomBreadcrumb :links="breadcrumbLinks" />
  </div>
  <Spinner v-if="isLoading"></Spinner>
  <UserForm :initialValues="user" :submitForm="submitForm" :isLoading="formLoading" v-else />
</template>

<script setup>
import { onMounted, ref } from 'vue'
import api from '@/api'
import { EMITTER_EVENTS } from '@/constants/emitterEvents.js'
import { useEmitter } from '@/composables/useEmitter'
import { handleHTTPError } from '@/utils/http'
import UserForm from '@/features/admin/users/UserForm.vue'
import { CustomBreadcrumb } from '@/components/ui/breadcrumb'
import { Spinner } from '@/components/ui/spinner'

const user = ref({})
const isLoading = ref(false)
const formLoading = ref(false)
const emitter = useEmitter()

const breadcrumbLinks = [
  { path: 'user-list', label: 'Users' },
  { path: '', label: 'Edit user' }
]

const submitForm = (values) => {
  updateUser(values)
}

const updateUser = async (payload) => {
  try {
    formLoading.value = true
    await api.updateUser(user.value.id, payload)
    emitter.emit(EMITTER_EVENTS.SHOW_TOAST, {
      title: "Success",
      description: "User updated successfully"
    })
  } catch (error) {
    emitter.emit(EMITTER_EVENTS.SHOW_TOAST, {
      title: 'Error',
      variant: 'destructive',
      description: handleHTTPError(error).message
    })
  } finally {
    formLoading.value = false
  }
}

onMounted(async () => {
  try {
    isLoading.value = true
    const resp = await api.getUser(props.id)
    user.value = resp.data.data
  } catch (error) {
    emitter.emit(EMITTER_EVENTS.SHOW_TOAST, {
      title: 'Error',
      variant: 'destructive',
      description: handleHTTPError(error).message
    })
  } finally {
    isLoading.value = false
  }
})

const props = defineProps({
  id: {
    type: String,
    required: true
  }
})
</script>
