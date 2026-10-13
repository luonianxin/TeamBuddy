<template>
  <div @contextmenu="openMenu">
    <slot />
    <Teleport to="body">
      <a-dropdown
        v-model:open="open"
        :trigger="['click']"
        :align="{ offset: [0, 0] }"
        @overlay-click="closeMenu"
      >
        <span
          class="blank-add-anchor"
          :style="{ left: `${position.x}px`, top: `${position.y}px` }"
          aria-hidden="true"
        />
        <template #overlay>
          <KnowledgeAddMenu
            :folder-creating="folderCreating"
            @select="closeMenu"
            @create-document="emit('create-document')"
            @create-folder="emit('create-folder')"
          />
        </template>
      </a-dropdown>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { nextTick, reactive, ref, watch } from 'vue'
import KnowledgeAddMenu from '@/views/knowledge/components/KnowledgeAddMenu.vue'

const props = defineProps<{ disabled?: boolean; folderCreating?: boolean }>()
const emit = defineEmits<{
  'create-document': []
  'create-folder': []
}>()
const open = ref(false)
const position = reactive({ x: 0, y: 0 })

async function openMenu(event: MouseEvent) {
  if (props.disabled) return
  const target = event.target
  if (!(target instanceof Element) || target.closest('[data-knowledge-add-ignore], button, input, select, textarea, a, [role="button"]')) return
  event.preventDefault()
  event.stopPropagation()
  // 先关闭再更新锚点，让连续右击空白处时浮层重新按点击位置对齐。
  open.value = false
  position.x = event.clientX
  position.y = event.clientY
  await nextTick()
  if (!props.disabled) open.value = true
}

function closeMenu() {
  open.value = false
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') closeMenu()
}

watch(() => props.disabled, (disabled) => {
  if (disabled) closeMenu()
})

watch(open, (visible, _, onCleanup) => {
  if (!visible) return
  document.addEventListener('scroll', closeMenu, true)
  window.addEventListener('resize', closeMenu)
  window.addEventListener('keydown', handleKeydown)
  onCleanup(() => {
    document.removeEventListener('scroll', closeMenu, true)
    window.removeEventListener('resize', closeMenu)
    window.removeEventListener('keydown', handleKeydown)
  })
})
</script>

<style scoped>
.blank-add-anchor {
  position: fixed;
  width: 1px;
  height: 1px;
  pointer-events: none;
}
</style>
