<script setup>
import { Primitive } from 'radix-vue'
import { buttonVariants } from '.'
import { cn } from '@/lib/utils'
import { ref, computed } from 'vue'
import { DotLoader } from '@/components/ui/loader'

const props = defineProps({
  variant: { type: null, required: false },
  size: { type: null, required: false },
  class: { type: null, required: false },
  asChild: { type: Boolean, required: false },
  as: { type: null, required: false, default: 'button' },
  isLoading: { type: Boolean, required: false, default: false }
})

const isDisabled = ref(false)

const computedClass = computed(() => {
  return cn(buttonVariants({ variant: props.variant, size: props.size }), props.class, {
    'cursor-not-allowed opacity-50': props.isLoading || isDisabled.value
  })
})
</script>

<template>
  <Primitive
    :as="as"
    :as-child="asChild"
    :class="computedClass"
    :disabled="isLoading || isDisabled"
  >
    <DotLoader v-if="isLoading" />
    <slot v-else />
  </Primitive>
</template>
