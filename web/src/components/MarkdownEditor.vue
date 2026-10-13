<template>
  <div
    ref="rootRef"
    class="markdown-editor"
    :class="{ 'is-disabled': disabled, 'is-toolbar-hidden': hideToolbar }"
  >
    <div
      ref="hostRef"
      class="markdown-editor-host"
    ></div>
    <!--
      S-UI-11: 浮动工具栏（仅 floatingToolbar === true 时启用）。
      只在真正选中文本（非空选区）时才显示，折叠光标或选区离开编辑器都隐藏，
      避免光标停留时工具栏一直悬浮遮挡编辑区；
      定位基于选区 Range 与组件根矩形，不依赖 vditor 私有 API；
      不使用 v-show 之外的显隐控制，未定位完成前以 opacity 隐藏，避免位置跳变（边界 E16）。
    -->
    <div
      v-show="floatingVisible"
      ref="floatingRef"
      class="floating-toolbar"
      :class="{ 'is-ready': floatingReady }"
      role="toolbar"
      :aria-label="t('components.markdown.floatingToolbar')"
      :style="floatingStyle"
    >
      <div class="floating-menu-anchor">
        <button
          type="button"
          class="floating-tool floating-type-trigger"
          :aria-label="t('components.markdown.blockType')"
          :aria-expanded="typeMenuOpen"
          @pointerdown.prevent.stop="toggleTypeMenu"
          @keydown.enter.prevent="toggleTypeMenu"
          @keydown.space.prevent="toggleTypeMenu"
        >
          <span class="floating-type-label">
            <img
              v-if="currentBlockIconSrc"
              :src="currentBlockIconSrc"
              alt=""
              class="block-type-icon block-type-image-icon"
            />
            <component
              :is="currentBlockIcon"
              v-else-if="currentBlockIcon"
              class="block-type-icon"
            />
            <template v-else>{{ currentBlockLabel }}</template>
          </span>
          <DownOutlined class="floating-type-chevron" />
        </button>
        <div
          v-if="typeMenuOpen"
          class="floating-popover block-type-menu"
          role="menu"
          @pointerdown.stop
        >
          <button
            v-for="item in BLOCK_TYPE_ITEMS"
            :key="item.key"
            type="button"
            class="block-type-menu-item"
            role="menuitem"
            @pointerdown.prevent="applyBlockType(item.key)"
            @keydown.enter.prevent="applyBlockType(item.key)"
            @keydown.space.prevent="applyBlockType(item.key)"
          >
            <span class="block-type-menu-icon">
              <img
                v-if="item.iconSrc"
                :src="item.iconSrc"
                alt=""
                class="block-type-icon block-type-image-icon"
              />
              <component
                :is="item.icon"
                v-else-if="item.icon"
                class="block-type-icon"
              />
              <template v-else>{{ item.shortLabel }}</template>
            </span>
            <span>{{ t(item.label) }}</span>
          </button>
        </div>
      </div>
      <!--
        工具按钮使用 `pointerdown.prevent` 以保留编辑器选区（点击时不让编辑区失焦）；
        该处理与 S-UI-12「不注册 contextmenu 处理器、不阻止右键默认行为」无关。
      -->
      <button
        v-for="tool in FLOATING_TOOLS"
        :key="tool.key"
        type="button"
        class="floating-tool"
        :aria-label="t(tool.label)"
        @pointerdown.prevent="applyFloatingTool(tool.key)"
        @keydown.enter.prevent="applyFloatingTool(tool.key)"
        @keydown.space.prevent="applyFloatingTool(tool.key)"
      >
        <component :is="tool.icon" />
      </button>
      <div class="floating-menu-anchor">
        <button
          type="button"
          class="floating-tool floating-color-trigger"
          :aria-label="t('components.markdown.textColor')"
          aria-haspopup="menu"
          :aria-expanded="colorMenuOpen"
          @pointerdown.prevent.stop="toggleColorMenu"
          @keydown.enter.prevent="toggleColorMenu"
          @keydown.space.prevent="toggleColorMenu"
        >
          <span
            class="floating-color-letter"
            aria-hidden="true"
          >
            {{ TEXT_COLOR_ICON }}
          </span>
          <DownOutlined class="floating-type-chevron" />
        </button>
        <div
          v-if="colorMenuOpen"
          class="floating-popover text-color-menu"
          role="menu"
          :aria-label="t('components.markdown.textColor')"
          @pointerdown.stop
        >
          <button
            v-for="item in TEXT_COLOR_ITEMS"
            :key="item.key"
            type="button"
            class="text-color-item"
            :class="{ 'is-selected': selectedTextColor === item.value }"
            role="menuitemradio"
            :aria-checked="selectedTextColor === item.value"
            :aria-label="t(item.label)"
            :title="t(item.label)"
            @pointerdown.prevent="applyTextColor(item)"
            @keydown.enter.prevent="applyTextColor(item)"
            @keydown.space.prevent="applyTextColor(item)"
          >
            <span
              :class="['text-color-swatch', `is-${item.key}`]"
              aria-hidden="true"
            >
              {{ TEXT_COLOR_ICON }}
            </span>
          </button>
        </div>
      </div>
      <template v-if="$slots['floating-actions']">
        <span class="floating-divider" />
        <span class="floating-actions">
          <slot
            name="floating-actions"
            :text="floatingSelectionText"
          />
        </span>
      </template>
    </div>

    <!-- 行左侧 block-handle 暂不展示；保留实现，便于后续重新启用。 -->
    <div
      v-if="BLOCK_HANDLE_ENABLED && blockHandleVisible"
      class="block-handle"
      :class="{ 'is-ready': blockHandleReady }"
      :style="blockHandleStyle"
    >
      <button
        type="button"
        class="block-handle-trigger"
        :aria-label="t('components.markdown.blockMenu')"
        :aria-expanded="blockMenuOpen"
        @pointerdown.prevent.stop="blockMenuOpen = !blockMenuOpen"
        @keydown.enter.prevent="blockMenuOpen = !blockMenuOpen"
        @keydown.space.prevent="blockMenuOpen = !blockMenuOpen"
      >
        <span class="block-handle-type">
          <img
            v-if="currentBlockIconSrc"
            :src="currentBlockIconSrc"
            alt=""
            class="block-type-icon block-type-image-icon"
          />
          <component
            :is="currentBlockIcon"
            v-else-if="currentBlockIcon"
            class="block-type-icon"
          />
          <template v-else>{{ currentBlockLabel }}</template>
        </span>
        <HolderOutlined />
      </button>
      <div
        v-if="blockMenuOpen"
        class="floating-popover block-menu"
        role="menu"
        @pointerdown.stop
      >
        <div
          class="block-type-grid"
          :aria-label="t('components.markdown.blockType')"
        >
          <button
            v-for="item in BLOCK_MENU_TYPE_ITEMS"
            :key="item.key"
            type="button"
            class="block-type-grid-item"
            :aria-label="t(item.label)"
            :title="t(item.label)"
            @pointerdown.prevent="applyBlockType(item.key)"
            @keydown.enter.prevent="applyBlockType(item.key)"
            @keydown.space.prevent="applyBlockType(item.key)"
          >
            <img
              v-if="item.iconSrc"
              :src="item.iconSrc"
              alt=""
              class="block-type-icon block-type-image-icon"
            />
            <component
              :is="item.icon"
              v-else-if="item.icon"
              class="block-type-icon"
            />
            <template v-else>{{ item.shortLabel }}</template>
          </button>
        </div>
        <div class="block-menu-divider" />
        <button
          type="button"
          class="block-menu-action"
          @pointerdown.prevent="performBlockAction('copy')"
          @keydown.enter.prevent="performBlockAction('copy')"
          @keydown.space.prevent="performBlockAction('copy')"
        >
          <CopyOutlined />
          <span>{{ t('components.markdown.copy') }}</span>
        </button>
        <button
          type="button"
          class="block-menu-action"
          @pointerdown.prevent="performBlockAction('duplicate')"
          @keydown.enter.prevent="performBlockAction('duplicate')"
          @keydown.space.prevent="performBlockAction('duplicate')"
        >
          <SnippetsOutlined />
          <span>{{ t('components.markdown.duplicate') }}</span>
        </button>
        <button
          type="button"
          class="block-menu-action"
          @pointerdown.prevent="performBlockAction('cut')"
          @keydown.enter.prevent="performBlockAction('cut')"
          @keydown.space.prevent="performBlockAction('cut')"
        >
          <ScissorOutlined />
          <span>{{ t('components.markdown.cut') }}</span>
        </button>
        <button
          type="button"
          class="block-menu-action"
          @pointerdown.prevent="invokeNativeTool('indent')"
          @keydown.enter.prevent="invokeNativeTool('indent')"
          @keydown.space.prevent="invokeNativeTool('indent')"
        >
          <MenuUnfoldOutlined />
          <span>{{ t('components.markdown.indent') }}</span>
        </button>
        <button
          type="button"
          class="block-menu-action"
          @pointerdown.prevent="invokeNativeTool('outdent')"
          @keydown.enter.prevent="invokeNativeTool('outdent')"
          @keydown.space.prevent="invokeNativeTool('outdent')"
        >
          <MenuFoldOutlined />
          <span>{{ t('components.markdown.outdent') }}</span>
        </button>
        <div class="block-menu-divider" />
        <button
          type="button"
          class="block-menu-action block-menu-action-danger"
          @pointerdown.prevent="performBlockAction('delete')"
          @keydown.enter.prevent="performBlockAction('delete')"
          @keydown.space.prevent="performBlockAction('delete')"
        >
          <DeleteOutlined />
          <span>{{ t('components.markdown.delete') }}</span>
        </button>
      </div>
    </div>

    <div
      v-if="linkDialogOpen"
      ref="linkDialogRef"
      class="link-dialog"
      role="dialog"
      aria-modal="false"
      :aria-label="t('components.markdown.insertLink')"
      :style="linkDialogStyle"
      @pointerdown.stop
    >
      <div class="link-dialog-header">
        <strong>{{ t('components.markdown.insertLink') }}</strong>
        <button
          type="button"
          class="link-dialog-close"
          :aria-label="t('components.markdown.close')"
          @click="closeLinkDialog"
        >
          <CloseOutlined />
        </button>
      </div>
      <label class="link-dialog-field">
        <span>{{ t('components.markdown.linkText') }}</span>
        <input
          v-model="linkText"
          type="text"
        />
      </label>
      <label class="link-dialog-field">
        <span>{{ t('components.markdown.linkUrl') }}</span>
        <input
          ref="linkUrlInputRef"
          v-model="linkUrl"
          type="text"
          :placeholder="t('components.markdown.linkUrlPlaceholder')"
          @keydown.enter.prevent="confirmLink"
          @keydown.esc.prevent="closeLinkDialog"
        />
      </label>
      <div class="link-dialog-actions">
        <button
          type="button"
          class="link-dialog-button"
          @click="closeLinkDialog"
        >
          {{ t('components.markdown.cancel') }}
        </button>
        <button
          type="button"
          class="link-dialog-button link-dialog-button-primary"
          :disabled="!linkText.trim() || !linkUrl.trim()"
          @click="confirmLink"
        >
          {{ t('components.markdown.confirm') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch, type Component } from 'vue'
import Vditor from 'vditor'
import 'vditor/dist/index.css'
import {
  BoldOutlined,
  CloseOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownOutlined,
  HolderOutlined,
  ItalicOutlined,
  LinkOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  OrderedListOutlined,
  ScissorOutlined,
  SnippetsOutlined,
  StrikethroughOutlined,
  UnorderedListOutlined,
} from '@ant-design/icons-vue'
import { Modal } from 'ant-design-vue'
import { MAX_IMAGE_SIZE, TASK_IMAGE_ACCEPT } from '@/composables/useTaskImageAttachments'
import { useAppI18n } from '@/i18n'
import markdownHighlightBlockIcon from '@/assets/icons/markdown-highlight-block.svg'
import markdownQuoteIcon from '@/assets/icons/markdown-quote.svg'
import markdownTaskListIcon from '@/assets/icons/markdown-task-list.svg'

const { t, locale } = useAppI18n()

const props = withDefaults(
  defineProps<{
    modelValue?: string
    placeholder?: string
    disabled?: boolean
    cacheId?: string
    onUploadImages?: (files: File[]) => Promise<string[]>
    /** S-UI-11: 是否启用跟随编写位置的浮动工具栏（默认 false，保持任务域既有调用方行为不变） */
    floatingToolbar?: boolean
    /** 仅由需要沉浸式文档画布的页面启用；其他调用方继续显示 Vditor 工具栏。 */
    hideToolbar?: boolean
    /** 首个 H1 内容为空时显示的视觉占位；不会写入 Markdown。 */
    titlePlaceholder?: string
    /** 编辑时始终保留首个 H1；标题块被删除时自动补回。 */
    ensureTitleHeading?: boolean
    /** 将视觉空白段落编码为可被 Vditor/Lute 往返保留的 Markdown 行。 */
    preserveBlankLines?: boolean
  }>(),
  {
    modelValue: '',
    placeholder: '',
    disabled: false,
    cacheId: '',
    floatingToolbar: false,
    hideToolbar: false,
    titlePlaceholder: '',
    ensureTitleHeading: false,
    preserveBlankLines: false,
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
  ready: []
  /** S-UI-13: 编辑器选区变化；`text` 为空表示选区已离开编辑器或已折叠 */
  'selection-change': [info: EditorSelectionInfo]
}>()

type EditorSelectionInfo = {
  text: string
  prefix: string
  suffix: string
  rect: DOMRect | null
}

type FloatingToolKey = 'bold' | 'italic' | 'strike' | 'inlineCode' | 'link'

type TextColorKey = 'neutral' | 'success' | 'warning' | 'error' | 'primary'

type TextColorItem = {
  key: TextColorKey
  value: string
  label: string
}

type BlockTypeKey =
  | 'text'
  | 'heading1'
  | 'heading2'
  | 'heading3'
  | 'heading4'
  | 'heading5'
  | 'heading6'
  | 'unorderedList'
  | 'orderedList'
  | 'checkList'
  | 'highlightBlock'
  | 'quote'
  | 'codeBlock'

type BlockActionKey = 'copy' | 'duplicate' | 'cut' | 'delete'

type BlockTypeItem = {
  key: BlockTypeKey
  label: string
  shortLabel: string
  icon?: Component
  iconSrc?: string
}

const rootRef = ref<HTMLElement>()
const hostRef = ref<HTMLElement>()
const floatingRef = ref<HTMLElement>()
const linkDialogRef = ref<HTMLElement>()
const linkUrlInputRef = ref<HTMLInputElement>()
let editor: Vditor | undefined
let applyingExternalValue = false
let destroyed = false
let editorReady = false
let editorVersion = 0

const VDITOR_CDN = `${import.meta.env.BASE_URL}vditor`.replace(/\/$/, '')

// 选区前后文定位片段长度（CX-2：DOM 级前后各 ≤32 字，不做真实行号）
const SELECTION_CONTEXT_LENGTH = 32
// 浮动工具栏与选区之间的最小间距，同时用作容器内边距约束（边界 E16）
const FLOATING_GAP = 8
// CommonMark 会忽略多余空行；NBSP 让视觉空白段落拥有稳定内容，同时保持页面上不可见。
const PRESERVED_BLANK_LINE = '\u00a0'
// 产品暂时隐藏整块行左侧操作入口；保留代码，后续可直接恢复。
const BLOCK_HANDLE_ENABLED = false

const FLOATING_TOOLS: Array<{ key: FloatingToolKey; label: string; icon: Component }> = [
  { key: 'bold', label: 'components.markdown.bold', icon: BoldOutlined },
  { key: 'italic', label: 'components.markdown.italic', icon: ItalicOutlined },
  { key: 'strike', label: 'components.markdown.strike', icon: StrikethroughOutlined },
  { key: 'link', label: 'components.markdown.link', icon: LinkOutlined },
  { key: 'inlineCode', label: 'components.markdown.inlineCode', icon: CodeOutlined },
]

const TEXT_COLOR_ITEMS: TextColorItem[] = [
  { key: 'neutral', value: '#262626', label: 'components.markdown.textColorNeutral' },
  { key: 'success', value: '#16a34a', label: 'components.markdown.textColorGreen' },
  { key: 'warning', value: '#ed744a', label: 'components.markdown.textColorOrange' },
  { key: 'error', value: '#fb363f', label: 'components.markdown.textColorRed' },
  { key: 'primary', value: '#3157e2', label: 'components.markdown.textColorBlue' },
]

const TEXT_COLOR_ICON = 'A'
const TEXT_COLOR_BY_VALUE = new Map(TEXT_COLOR_ITEMS.map((item) => [item.value, item]))

const BLOCK_TYPE_ITEMS: BlockTypeItem[] = [
  { key: 'text', label: 'components.markdown.text', shortLabel: 'T' },
  { key: 'heading1', label: 'components.markdown.heading1', shortLabel: 'H₁' },
  { key: 'heading2', label: 'components.markdown.heading2', shortLabel: 'H₂' },
  { key: 'heading3', label: 'components.markdown.heading3', shortLabel: 'H₃' },
  { key: 'heading4', label: 'components.markdown.heading4', shortLabel: 'H₄' },
  { key: 'heading5', label: 'components.markdown.heading5', shortLabel: 'H₅' },
  { key: 'heading6', label: 'components.markdown.heading6', shortLabel: 'H₆' },
  {
    key: 'unorderedList',
    label: 'components.markdown.unorderedList',
    shortLabel: '•',
    icon: UnorderedListOutlined,
  },
  {
    key: 'orderedList',
    label: 'components.markdown.orderedList',
    shortLabel: '1.',
    icon: OrderedListOutlined,
  },
  {
    key: 'quote',
    label: 'components.markdown.quote',
    shortLabel: '“',
    iconSrc: markdownQuoteIcon,
  },
  { key: 'codeBlock', label: 'components.markdown.codeBlock', shortLabel: '</>' },
]

const BLOCK_MENU_TYPE_ITEMS: BlockTypeItem[] = [
  ...BLOCK_TYPE_ITEMS.slice(0, 9),
  {
    key: 'checkList',
    label: 'components.markdown.checkList',
    shortLabel: '☑',
    iconSrc: markdownTaskListIcon,
  },
  {
    key: 'highlightBlock',
    label: 'components.markdown.highlightBlock',
    shortLabel: '▰',
    iconSrc: markdownHighlightBlockIcon,
  },
  ...BLOCK_TYPE_ITEMS.slice(9),
]

const floatingVisible = ref(false)
const floatingReady = ref(false)
const floatingSelectionText = ref('')
const floatingStyle = ref<Record<string, string>>({})
const typeMenuOpen = ref(false)
const colorMenuOpen = ref(false)
const selectedTextColor = ref(TEXT_COLOR_ITEMS[0].value)
const blockHandleVisible = ref(false)
const blockHandleReady = ref(false)
const blockHandleStyle = ref<Record<string, string>>({})
const blockMenuOpen = ref(false)
const currentBlockType = ref<BlockTypeKey>('text')
const currentBlockLabel = ref('T')
const currentBlockIcon = shallowRef<Component | null>(null)
const currentBlockIconSrc = ref('')
const linkDialogOpen = ref(false)
const linkText = ref('')
const linkUrl = ref('')
const linkDialogStyle = ref<Record<string, string>>({})
let floatingSequence = 0
let lastEmittedSelectionKey = ''
let savedSelectionRange: Range | null = null
let activeBlockElement: HTMLElement | null = null
let activeLineRange: Range | null = null
let textColorDecorationFrame = 0
let titlePlaceholderFrame = 0
let titleHeadingFrame = 0
let textColorMarkerObserver: MutationObserver | null = null

function preserveSourceBlankLines(value: string) {
  if (!props.preserveBlankLines || !/\r?\n\r?\n/.test(value)) return value
  const lines = value.split(/\r?\n/)
  let fenceMarker = ''
  let fenceLength = 0
  let seenContent = false
  let inFrontMatter = lines[0]?.trim() === '---'

  for (let index = 0; index < lines.length;) {
    const line = lines[index]
    if (inFrontMatter) {
      if (index > 0 && /^(?:---|\.\.\.)\s*$/.test(line)) inFrontMatter = false
      index += 1
      continue
    }
    const fence = /^ {0,3}(`{3,}|~{3,})/.exec(line)
    if (fenceMarker) {
      if (
        fence &&
        fence[1][0] === fenceMarker &&
        fence[1].length >= fenceLength &&
        line.slice(fence[0].length).trim() === ''
      ) {
        fenceMarker = ''
        fenceLength = 0
      }
      index += 1
      continue
    }
    if (fence) {
      fenceMarker = fence[1][0]
      fenceLength = fence[1].length
      seenContent = true
      index += 1
      continue
    }
    if (line.trim()) {
      seenContent = true
      index += 1
      continue
    }

    let end = index + 1
    while (end < lines.length && !lines[end].trim()) end += 1
    // 一个空行是 Markdown 块分隔符；其后的空行才代表用户有意插入的空白段落。
    if (seenContent && end - index > 1) {
      for (let blankIndex = index + 1; blankIndex < end; blankIndex += 1) {
        lines[blankIndex] = PRESERVED_BLANK_LINE
      }
    }
    index = end
  }
  return lines.join('\n')
}

function serializedEditorValue() {
  if (!editorReady || !editor) return preserveSourceBlankLines(props.modelValue)
  if (!props.preserveBlankLines) return editor.getValue()
  const root = editorContentRoot()
  if (!root) return preserveSourceBlankLines(editor.getValue())

  const clone = root.cloneNode(true) as HTMLElement
  Array.from(clone.children).forEach((block) => {
    if (!(block instanceof HTMLElement) || block.tagName !== 'P') return
    const content = block.cloneNode(true) as HTMLElement
    content.querySelectorAll('.vditor-ir__marker, wbr, br').forEach((node) => node.remove())
    const visibleText = (content.textContent || '').replace(/[\u00a0\u200b]/g, '').trim()
    const hasVisualContent = Boolean(
      content.querySelector('img, video, audio, iframe, input, svg, canvas, math, table, hr, pre'),
    )
    if (!visibleText && !hasVisualContent) block.textContent = PRESERVED_BLANK_LINE
  })
  return editor.vditor.lute.VditorIRDOM2Md(clone.innerHTML)
}

function editorInputValue(value: string) {
  return preserveSourceBlankLines(value || '')
}

function currentValue() {
  return serializedEditorValue()
}

function syncFromEditor() {
  if (!editorReady || !editor || applyingExternalValue) return
  emit('update:modelValue', serializedEditorValue())
}

function handleUpload(files: File[]): string | null | Promise<null> {
  if (!props.onUploadImages) return t('components.markdown.uploadUnavailable')
  if (!files.length) return null
  return insertUploadedMarkers(files)
}

async function insertUploadedMarkers(files: File[]): Promise<null> {
  const onUploadImages = props.onUploadImages
  if (!onUploadImages) {
    editor?.tip(t('components.markdown.uploadUnavailable'))
    return null
  }
  try {
    const markers = await onUploadImages(files)
    if (markers.length) {
      editor?.insertValue(markers.join('\n'))
      syncFromEditor()
    }
  } catch (error) {
    editor?.tip(error instanceof Error ? error.message : t('components.markdown.uploadFailed'))
  }
  return null
}

function createEditor() {
  const host = hostRef.value
  if (!host || destroyed) return
  const version = ++editorVersion

  editor = new Vditor(host, {
    cdn: VDITOR_CDN,
    mode: 'ir',
    theme: 'classic',
    icon: 'ant',
    lang: locale.value === 'en-US' ? 'en_US' : 'zh_CN',
    height: '100%',
    placeholder: props.placeholder || t('components.markdown.placeholder'),
    value: editorInputValue(props.modelValue),
    cache: { enable: false, id: props.cacheId || 'task-description' },
    // S-UI-10: 顶部固定工具栏保留（与浮动工具栏并存）
    toolbar: [
      'headings',
      'bold',
      'italic',
      'strike',
      '|',
      'list',
      'ordered-list',
      'check',
      'outdent',
      'indent',
      '|',
      'quote',
      'line',
      'code',
      'inline-code',
      'link',
      'table',
      '|',
      'undo',
      'redo',
      ...(props.onUploadImages ? (['|', 'upload'] as const) : []),
    ],
    toolbarConfig: {
      pin: true,
      hide: props.hideToolbar,
    },
    counter: { enable: false },
    outline: { enable: false, position: 'left' },
    preview: {
      delay: 200,
      hljs: { enable: false, lineNumber: true },
      // 让 setPadding 计算出的左右内边距落到 minPadding=35px，从而让正文撑满编辑器宽度。
      // 设成足够大的值后，(editorWidth - maxWidth) / 2 必为负，再被 Math.max(35, ...) 夹回 35。
      maxWidth: 99999,
      markdown: {
        toc: false,
        footnotes: false,
        mark: true,
        sanitize: true,
        codeBlockPreview: false,
        mathBlockPreview: false,
      },
      theme: {
        current: 'light',
        path: `${VDITOR_CDN}/dist/css/content-theme`,
      },
    },
    upload: {
      accept: TASK_IMAGE_ACCEPT,
      max: MAX_IMAGE_SIZE,
      multiple: true,
      filename: (name) => name,
      handler: handleUpload,
    },
    input() {
      ensureRequiredTitleHeading()
      hideTextColorSourceMarkers()
      syncFromEditor()
      queueTextColorDecoration()
      queueTitlePlaceholderDecoration()
    },
    blur() {
      syncFromEditor()
      window.setTimeout(() => {
        const root = rootRef.value
        if (
          root &&
          !root.contains(document.activeElement) &&
          !linkDialogOpen.value &&
          !typeMenuOpen.value &&
          !colorMenuOpen.value &&
          !blockMenuOpen.value
        ) {
          hideEditorOverlays()
        }
      }, 0)
    },
    after() {
      if (destroyed || version !== editorVersion) return
      editorReady = true
      applyingExternalValue = true
      editor?.setValue(editorInputValue(props.modelValue), true)
      applyingExternalValue = false
      if (props.disabled) editor?.disabled()
      else editor?.enable()
      observeTextColorMarkers()
      queueTextColorDecoration()
      queueTitlePlaceholderDecoration()
      emit('ready')
    },
  })
}

/* ---------------- S-UI-11 / S-UI-13：浮动工具栏与选区信息 ---------------- */

/** 即时渲染模式下的正文容器（仅用渲染后的 DOM 类名做定位，不使用 vditor 私有 API） */
function editorContentRoot(): HTMLElement | null {
  const host = hostRef.value
  if (!host) return null
  return (
    host.querySelector<HTMLElement>('.vditor-ir .vditor-reset') ||
    host.querySelector<HTMLElement>('.vditor-ir')
  )
}

/** 选区边界所在的块级元素（`.vditor-reset` 的直接子元素），用于取前后文片段 */
function selectionBlock(node: Node): HTMLElement | null {
  const root = editorContentRoot()
  if (!root) return null
  let current: Node | null = node
  while (current && current !== root) {
    if (current.parentNode === root && current instanceof HTMLElement) return current
    current = current.parentNode
  }
  return root
}

function offsetWithin(block: HTMLElement, boundary: Node, offset: number): number | null {
  const probe = document.createRange()
  probe.selectNodeContents(block)
  try {
    probe.setEnd(boundary, offset)
  } catch {
    return null
  }
  return probe.toString().length
}

function normalizeContext(value: string) {
  return value.replace(/\s+/g, ' ').trim()
}

function blockTypeOf(block: HTMLElement): BlockTypeKey {
  const tagName = block.tagName.toLowerCase()
  if (/^h[1-6]$/.test(tagName)) return `heading${tagName.slice(1)}` as BlockTypeKey
  if (tagName === 'blockquote') return 'quote'
  if (tagName === 'pre') return 'codeBlock'
  if (tagName === 'hr') return 'text'
  if (tagName === 'ol') return 'orderedList'
  if (tagName === 'ul') {
    return block.querySelector('input[type="checkbox"]') ? 'checkList' : 'unorderedList'
  }
  if (
    tagName === 'p' &&
    block.children.length === 1 &&
    block.firstElementChild?.getAttribute('data-type') === 'mark'
  ) {
    return 'highlightBlock'
  }
  return 'text'
}

function updateCurrentBlock(block: HTMLElement | null) {
  const root = editorContentRoot()
  activeBlockElement = block && block !== root ? block : null
  currentBlockType.value = activeBlockElement ? blockTypeOf(activeBlockElement) : 'text'
  const item = BLOCK_MENU_TYPE_ITEMS.find((candidate) => candidate.key === currentBlockType.value)
  currentBlockLabel.value = item?.shortLabel ?? 'T'
  currentBlockIcon.value = item?.icon ?? null
  currentBlockIconSrc.value = item?.iconSrc ?? ''
}

function saveSelection(range: Range) {
  savedSelectionRange = range.cloneRange()
  const startElement =
    range.startContainer instanceof Element
      ? range.startContainer
      : range.startContainer.parentElement
  const selectedColor = startElement
    ?.closest<HTMLElement>('.markdown-text-color')
    ?.dataset.goteamsColor?.toLowerCase()
  selectedTextColor.value =
    selectedColor && TEXT_COLOR_BY_VALUE.has(selectedColor)
      ? selectedColor
      : TEXT_COLOR_ITEMS[0].value
  updateCurrentBlock(selectionBlock(range.startContainer))
}

function restoreSavedSelection() {
  if (!savedSelectionRange) return false
  const root = editorContentRoot()
  if (!root || !root.contains(savedSelectionRange.commonAncestorContainer)) return false
  const selection = window.getSelection()
  if (!selection) return false
  selection.removeAllRanges()
  selection.addRange(savedSelectionRange.cloneRange())
  return true
}

function toggleTypeMenu() {
  typeMenuOpen.value = !typeMenuOpen.value
  if (typeMenuOpen.value) colorMenuOpen.value = false
}

function toggleColorMenu() {
  colorMenuOpen.value = !colorMenuOpen.value
  if (colorMenuOpen.value) typeMenuOpen.value = false
}

function hideBlockHandle() {
  blockHandleVisible.value = false
  blockHandleReady.value = false
  blockMenuOpen.value = false
}

function positionBlockHandle(block: HTMLElement | null, lineRect: DOMRect | null) {
  const host = rootRef.value
  if (!BLOCK_HANDLE_ENABLED || !host || !block || block === editorContentRoot()) {
    hideBlockHandle()
    return
  }
  const hostRect = host.getBoundingClientRect()
  const blockRect = block.getBoundingClientRect()
  // 块级编辑手柄需要完整让出正文起始位置；允许贴到容器左缘，避免遮挡标题或正文首字。
  const left = Math.max(0, blockRect.left - hostRect.left - 66)
  const maxTop = Math.max(FLOATING_GAP, hostRect.height - 32 - FLOATING_GAP)
  const targetTop = lineRect?.top || blockRect.top
  const top = Math.min(Math.max(targetTop - hostRect.top + 2, FLOATING_GAP), maxTop)
  blockHandleStyle.value = { left: `${Math.round(left)}px`, top: `${Math.round(top)}px` }
  blockHandleVisible.value = !props.disabled
  blockHandleReady.value = true
}

/**
 * Chromium 的 Selection.modify 能按实际排版后的可视行扩展折叠光标。
 * 块菜单据此只处理点击行；不支持该 API 时安全退化为当前 Markdown 块。
 */
function visualLineRangeAtCaret(range: Range, block: HTMLElement | null): Range | null {
  if (!range.collapsed || !block) return null
  const selection = window.getSelection()
  const modifiableSelection = selection as Selection & {
    modify?: (alter: 'move' | 'extend', direction: 'backward' | 'forward', granularity: string) => void
  }
  if (!selection || typeof modifiableSelection.modify !== 'function') return null

  const originalRange = range.cloneRange()
  try {
    selection.removeAllRanges()
    selection.addRange(originalRange.cloneRange())
    modifiableSelection.modify('move', 'backward', 'lineboundary')
    modifiableSelection.modify('extend', 'forward', 'lineboundary')
    if (!selection.rangeCount) return null
    const lineRange = selection.getRangeAt(0).cloneRange()
    const startInside = block.contains(lineRange.startContainer)
    const endInside = block.contains(lineRange.endContainer)
    if (!startInside) lineRange.setStart(block, 0)
    if (!endInside) lineRange.setEnd(block, block.childNodes.length)
    return lineRange.collapsed ? null : lineRange
  } finally {
    selection.removeAllRanges()
    selection.addRange(originalRange)
  }
}

/** 折叠光标位于空行/行首时可能得到全零矩形，退化为块级元素矩形（边界 E16） */
function readRangeRect(range: Range): DOMRect | null {
  const rect = range.getBoundingClientRect()
  if (rect.width || rect.height || rect.top || rect.left) return rect
  const rects = range.getClientRects()
  if (rects.length) return rects[0]
  return selectionBlock(range.startContainer)?.getBoundingClientRect() ?? null
}

function readEditorSelection(): EditorSelectionInfo | null {
  const root = editorContentRoot()
  const selection = window.getSelection()
  if (!root || !selection || !selection.rangeCount) return null
  const range = selection.getRangeAt(0)
  const container = range.commonAncestorContainer
  const element = container instanceof Element ? container : container.parentElement
  if (!element || !root.contains(element)) return null
  saveSelection(range)
  if (range.collapsed) {
    activeLineRange = visualLineRangeAtCaret(range, activeBlockElement)
    positionBlockHandle(activeBlockElement, readRangeRect(range))
  } else {
    activeLineRange = null
    hideBlockHandle()
  }

  let prefix = ''
  let suffix = ''
  const startBlock = selectionBlock(range.startContainer)
  if (startBlock) {
    const startOffset = offsetWithin(startBlock, range.startContainer, range.startOffset)
    if (startOffset !== null) {
      const blockText = startBlock.textContent || ''
      prefix = normalizeContext(
        blockText.slice(Math.max(0, startOffset - SELECTION_CONTEXT_LENGTH), startOffset),
      )
    }
  }
  const endBlock = selectionBlock(range.endContainer)
  if (endBlock) {
    const endOffset = offsetWithin(endBlock, range.endContainer, range.endOffset)
    if (endOffset !== null) {
      const blockText = endBlock.textContent || ''
      suffix = normalizeContext(blockText.slice(endOffset, endOffset + SELECTION_CONTEXT_LENGTH))
    }
  }
  return { text: selection.toString(), prefix, suffix, rect: readRangeRect(range) }
}

function emitSelectionChange(info: EditorSelectionInfo | null) {
  const key = info ? `${info.text}#${info.rect ? 1 : 0}` : 'none'
  if (key === lastEmittedSelectionKey) return
  lastEmittedSelectionKey = key
  emit('selection-change', info ?? { text: '', prefix: '', suffix: '', rect: null })
}

function positionFloatingToolbar(rect: DOMRect | null) {
  const host = rootRef.value
  const toolbar = floatingRef.value
  if (!host || !toolbar || !rect) return
  const hostRect = host.getBoundingClientRect()
  const width = toolbar.offsetWidth
  const height = toolbar.offsetHeight

  // 水平：默认与选区左缘对齐；右侧越界则改为右缘对齐；仍越界则贴住容器右侧
  let left = rect.left - hostRect.left
  if (left + width > hostRect.width - FLOATING_GAP) {
    left = rect.right - hostRect.left - width
  }
  const maxLeft = Math.max(FLOATING_GAP, hostRect.width - width - FLOATING_GAP)
  left = Math.min(Math.max(left, FLOATING_GAP), maxLeft)

  // 垂直：默认浮在选区上方；上方空间不足则翻转到选区下方（边界 E16）
  let top = rect.top - hostRect.top - height - FLOATING_GAP
  if (top < FLOATING_GAP) top = rect.bottom - hostRect.top + FLOATING_GAP
  const maxTop = Math.max(FLOATING_GAP, hostRect.height - height - FLOATING_GAP)
  top = Math.min(Math.max(top, FLOATING_GAP), maxTop)

  floatingStyle.value = { left: `${Math.round(left)}px`, top: `${Math.round(top)}px` }
}

function hideFloatingToolbar() {
  floatingSequence += 1
  floatingVisible.value = false
  floatingReady.value = false
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  floatingSelectionText.value = ''
  emitSelectionChange(null)
}

function hideEditorOverlays() {
  hideFloatingToolbar()
  hideBlockHandle()
  updateCurrentBlock(null)
  activeLineRange = null
  if (!linkDialogOpen.value) savedSelectionRange = null
}

/** 读取当前选区（光标或选中文本）并刷新浮层位置；只有非空选区才显示，折叠光标或选区不在编辑器内则隐藏 */
async function refreshFloatingToolbar() {
  if (!props.floatingToolbar || destroyed) return
  const sequence = ++floatingSequence
  const info = readEditorSelection()
  // 折叠光标（info.text 为空）也走隐藏分支：避免光标进入编辑器后工具栏一直悬浮遮挡编辑区，
  // 同时 hideFloatingToolbar 会向调用方 emit 空 text 的选区变化以便清理相关 UI 状态
  if (!info || !info.text) {
    hideFloatingToolbar()
    return
  }
  hideBlockHandle()
  floatingSelectionText.value = info.text
  if (!floatingVisible.value) floatingReady.value = false
  floatingVisible.value = true
  await nextTick()
  if (sequence !== floatingSequence || !floatingVisible.value) return
  positionFloatingToolbar(info.rect)
  floatingReady.value = true
  emitSelectionChange(info)
}

function handleDocumentSelectionChange() {
  void refreshFloatingToolbar()
}

function handleEditorScroll() {
  if (floatingVisible.value || blockHandleVisible.value) void refreshFloatingToolbar()
}

function currentSelection() {
  return editor?.getSelection() ?? ''
}

function nativeToolbarButton(name: string) {
  return hostRef.value?.querySelector<HTMLButtonElement>(
    `.vditor-toolbar button[data-type="${name}"]`,
  )
}

/** 复用 Vditor 自带命令，保留 IR 模式对嵌套格式、撤销栈和格式切换的处理。 */
function invokeNativeTool(name: string) {
  if (!editor || !restoreSavedSelection()) return
  const button = nativeToolbarButton(name)
  if (!button || button.classList.contains('vditor-menu--disabled')) return
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  blockMenuOpen.value = false
  button.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
  window.setTimeout(() => {
    syncFromEditor()
    void refreshFloatingToolbar()
  }, 0)
}

function applyFloatingTool(key: FloatingToolKey) {
  switch (key) {
    case 'bold':
      invokeNativeTool('bold')
      break
    case 'italic':
      invokeNativeTool('italic')
      break
    case 'strike':
      invokeNativeTool('strike')
      break
    case 'inlineCode':
      invokeNativeTool('inline-code')
      break
    case 'link':
      void openLinkDialog()
      break
  }
}

function inlineMarker(value: string) {
  const marker = document.createElement('span')
  marker.dataset.type = 'html-inline'
  marker.className = 'vditor-ir__node markdown-text-color-source-marker'
  const code = document.createElement('code')
  code.className = 'vditor-ir__marker vditor-ir__marker--hide'
  code.textContent = value
  marker.appendChild(code)
  return marker
}

function markerValue(node: Node | null) {
  if (!(node instanceof HTMLElement) || node.dataset.type !== 'html-inline') return ''
  return node.querySelector('code.vditor-ir__marker')?.textContent?.trim() ?? ''
}

function markerColor(node: Node) {
  const match = markerValue(node).match(/data-goteams-color=["'](#[0-9a-f]{6})["']/i)
  const value = match?.[1]?.toLowerCase()
  return value && TEXT_COLOR_BY_VALUE.has(value) ? value : ''
}

function closingColorMarker(opening: HTMLElement) {
  let sibling: Node | null = opening.nextSibling
  let nestedSpanDepth = 0
  while (sibling) {
    const value = markerValue(sibling)
    if (/^<span\b/i.test(value)) nestedSpanDepth += 1
    if (/^<\/span\s*>$/i.test(value)) {
      if (nestedSpanDepth === 0) return sibling as HTMLElement
      nestedSpanDepth -= 1
    }
    sibling = sibling.nextSibling
  }
  return null
}

function hideTextColorMarker(marker: HTMLElement) {
  if (!marker.classList.contains('markdown-text-color-source-marker')) {
    marker.classList.add('markdown-text-color-source-marker')
  }
  const code = marker.querySelector<HTMLElement>('.vditor-ir__marker')
  if (code && !code.classList.contains('vditor-ir__marker--hide')) {
    code.classList.add('vditor-ir__marker--hide')
  }
}

/** Vditor 输入时会重建当前行的 inline HTML 节点，需要在重建后立即恢复隐藏标记。 */
function hideTextColorSourceMarkers(root = editorContentRoot()) {
  if (!root) return
  const openings = Array.from(
    root.querySelectorAll<HTMLElement>('span[data-type="html-inline"]'),
  ).filter((node) => Boolean(markerColor(node)))
  openings.forEach((opening) => {
    hideTextColorMarker(opening)
    const closing = closingColorMarker(opening)
    if (closing) hideTextColorMarker(closing)
  })
}

function observeTextColorMarkers() {
  textColorMarkerObserver?.disconnect()
  textColorMarkerObserver = null
  const root = editorContentRoot()
  if (!root) return
  hideTextColorSourceMarkers(root)
  textColorMarkerObserver = new MutationObserver(() => hideTextColorSourceMarkers(root))
  textColorMarkerObserver.observe(root, {
    subtree: true,
    childList: true,
    characterData: true,
    attributes: true,
    attributeFilter: ['class'],
  })
}

function stopTextColorMarkerObserver() {
  textColorMarkerObserver?.disconnect()
  textColorMarkerObserver = null
}

function selectionElement(node: Node) {
  return node instanceof Element ? node : node.parentElement
}

function rangeIsAtElementBoundary(range: Range, element: HTMLElement, atStart: boolean) {
  const probe = document.createRange()
  probe.selectNodeContents(element)
  try {
    if (atStart) probe.setEnd(range.startContainer, range.startOffset)
    else probe.setStart(range.startContainer, range.startOffset)
  } catch {
    return false
  }
  return probe.toString() === ''
}

function visibleTextWithin(node: Node, fromEnd: boolean): Text | null {
  if (node instanceof Text) return node.data ? node : null
  if (!(node instanceof HTMLElement)) return null
  if (
    node.classList.contains('markdown-text-color-source-marker') ||
    node.classList.contains('vditor-ir__marker')
  ) {
    return null
  }
  const walker = document.createTreeWalker(node, NodeFilter.SHOW_TEXT, {
    acceptNode(candidate) {
      const parent = candidate.parentElement
      if (
        !candidate.textContent ||
        parent?.closest('.markdown-text-color-source-marker, .vditor-ir__marker')
      ) {
        return NodeFilter.FILTER_REJECT
      }
      return NodeFilter.FILTER_ACCEPT
    },
  })
  if (!fromEnd) return walker.nextNode() as Text | null
  let last: Text | null = null
  let current = walker.nextNode()
  while (current) {
    last = current as Text
    current = walker.nextNode()
  }
  return last
}

function adjacentVisibleText(anchor: Node, direction: 'previous' | 'next', root: HTMLElement) {
  let current: Node | null = anchor
  while (current && current !== root) {
    let sibling =
      direction === 'previous' ? current.previousSibling : current.nextSibling
    while (sibling) {
      const text = visibleTextWithin(sibling, direction === 'previous')
      if (text) return text
      sibling = direction === 'previous' ? sibling.previousSibling : sibling.nextSibling
    }
    current = current.parentNode
  }
  return null
}

function deleteVisibleCharacter(text: Text, backward: boolean, offset?: number) {
  const caretOffset = offset ?? (backward ? text.data.length : 0)
  const characters = Array.from(
    backward ? text.data.slice(0, caretOffset) : text.data.slice(caretOffset),
  )
  const character = backward ? characters.at(-1) : characters[0]
  if (!character) return false
  const start = backward ? caretOffset - character.length : caretOffset
  text.deleteData(start, character.length)
  const range = document.createRange()
  range.setStart(text, start)
  range.collapse(true)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)
  editor?.vditor.undo?.addToUndoStack(editor.vditor)
  syncFromEditor()
  queueTextColorDecoration()
  queueTitlePlaceholderDecoration()
  return true
}

/**
 * 隐藏的颜色标签仍是 contenteditable DOM 的一部分。光标位于彩色文字边界时，浏览器会优先
 * 删除标签字符；这里直接删除相邻的可见字符，再同步撤销栈和 Markdown，确保开闭标签成对保留。
 */
function handleTextColorDelete(event: KeyboardEvent) {
  if (
    props.disabled ||
    event.isComposing ||
    event.metaKey ||
    event.ctrlKey ||
    event.altKey ||
    (event.key !== 'Backspace' && event.key !== 'Delete')
  ) {
    return
  }
  const root = editorContentRoot()
  const selection = window.getSelection()
  if (!root || !selection?.rangeCount) return
  const range = selection.getRangeAt(0)
  if (!range.collapsed || !root.contains(range.startContainer)) return

  const startElement = selectionElement(range.startContainer)
  const sourceMarker = startElement?.closest<HTMLElement>('.markdown-text-color-source-marker')
  const colorWrapper = startElement?.closest<HTMLElement>('.markdown-text-color')
  let targetText: Text | null = null
  let targetOffset: number | undefined
  const deleteFromEnd = event.key === 'Backspace'
  let handlesBoundary = false

  if (sourceMarker) {
    const isOpening = Boolean(markerColor(sourceMarker))
    const isClosing = /^<\/span\s*>$/i.test(markerValue(sourceMarker))
    if (isOpening) {
      handlesBoundary = true
      const wrapper = sourceMarker.nextElementSibling
      if (event.key === 'Backspace') {
        targetText = adjacentVisibleText(sourceMarker, 'previous', root)
      } else if (wrapper instanceof HTMLElement && wrapper.classList.contains('markdown-text-color')) {
        targetText = visibleTextWithin(wrapper, false)
        if (!targetText) {
          const closing = wrapper.nextElementSibling
          targetText = closing ? adjacentVisibleText(closing, 'next', root) : null
        }
      }
    } else if (isClosing) {
      handlesBoundary = true
      const wrapper = sourceMarker.previousElementSibling
      if (
        event.key === 'Backspace' &&
        wrapper instanceof HTMLElement &&
        wrapper.classList.contains('markdown-text-color')
      ) {
        targetText = visibleTextWithin(wrapper, true)
        if (!targetText) {
          const opening = wrapper.previousElementSibling
          targetText = opening ? adjacentVisibleText(opening, 'previous', root) : null
        }
      } else if (event.key === 'Delete') {
        targetText = adjacentVisibleText(sourceMarker, 'next', root)
      }
    }
  } else if (colorWrapper) {
    if (
      event.key === 'Backspace' &&
      rangeIsAtElementBoundary(range, colorWrapper, true) &&
      colorWrapper.previousElementSibling?.classList.contains('markdown-text-color-source-marker')
    ) {
      handlesBoundary = true
      targetText = adjacentVisibleText(colorWrapper.previousElementSibling, 'previous', root)
    } else if (
      event.key === 'Delete' &&
      rangeIsAtElementBoundary(range, colorWrapper, false) &&
      colorWrapper.nextElementSibling?.classList.contains('markdown-text-color-source-marker')
    ) {
      handlesBoundary = true
      targetText = adjacentVisibleText(colorWrapper.nextElementSibling, 'next', root)
    } else if (range.startContainer instanceof Text) {
      handlesBoundary = true
      targetText = range.startContainer
      targetOffset = range.startOffset
    }
  }

  if (!handlesBoundary) return
  event.preventDefault()
  event.stopPropagation()
  if (targetText) deleteVisibleCharacter(targetText, deleteFromEnd, targetOffset)
}

/**
 * Vditor IR 会把原生 inline HTML 拆成隐藏标记与文本节点；这里仅为固定白名单颜色补充
 * 可视包装层。包装层不会进入 Markdown，源文档仍保存标准的 `<span style="color">`。
 */
function decorateTextColors() {
  const root = editorContentRoot()
  if (!root) return
  hideTextColorSourceMarkers(root)
  root.querySelectorAll<HTMLElement>('.markdown-text-color').forEach((wrapper) => {
    wrapper.replaceWith(...Array.from(wrapper.childNodes))
  })
  const openings = Array.from(
    root.querySelectorAll<HTMLElement>('span[data-type="html-inline"]'),
  ).filter((node) => Boolean(markerColor(node)))
  openings.forEach((opening) => {
    if (!opening.isConnected) return
    const item = TEXT_COLOR_BY_VALUE.get(markerColor(opening))
    const closing = closingColorMarker(opening)
    if (!item || !closing || opening.parentNode !== closing.parentNode) return
    const range = document.createRange()
    range.setStartAfter(opening)
    range.setEndBefore(closing)
    if (range.collapsed) return
    const wrapper = document.createElement('span')
    wrapper.className = `markdown-text-color is-${item.key}`
    wrapper.dataset.goteamsColor = item.value
    wrapper.appendChild(range.extractContents())
    range.insertNode(wrapper)
  })
}

function queueTextColorDecoration() {
  window.cancelAnimationFrame(textColorDecorationFrame)
  textColorDecorationFrame = window.requestAnimationFrame(decorateTextColors)
}

const TITLE_CARET_ANCHOR = '\u200b'

function ensureTitleCaretAnchor(heading: HTMLElement): Text {
  const existing = Array.from(heading.childNodes).find(
    (node): node is Text => node instanceof Text && node.data.includes(TITLE_CARET_ANCHOR),
  )
  if (existing) return existing
  const anchor = document.createTextNode(TITLE_CARET_ANCHOR)
  heading.append(anchor)
  return anchor
}

function titleHeadingIsEmpty(heading: HTMLElement): boolean {
  const headingContent = heading.cloneNode(true) as HTMLElement
  headingContent
    .querySelectorAll('.vditor-ir__marker, wbr, br')
    .forEach((node) => node.remove())
  return !(headingContent.textContent || '').replace(/[\u00a0\u200b]/g, '').trim()
}

function decorateTitlePlaceholder() {
  const root = editorContentRoot()
  if (!root) return
  root.querySelectorAll<HTMLElement>('[data-goteams-title-placeholder]').forEach((heading) => {
    delete heading.dataset.goteamsTitlePlaceholder
  })
  if (!props.titlePlaceholder) return
  const firstHeading = root.querySelector<HTMLElement>(':scope > h1')
  if (!firstHeading) return
  if (titleHeadingIsEmpty(firstHeading)) {
    // 标题 marker 在知识库页会被隐藏；空 H1 若没有可编辑文本节点，
    // 浏览器会把光标和输入落到下一个段落。零宽锚点会被 Vditor 序列化时忽略。
    ensureTitleCaretAnchor(firstHeading)
    firstHeading.dataset.goteamsTitlePlaceholder = props.titlePlaceholder
  }
}

function queueTitlePlaceholderDecoration() {
  window.cancelAnimationFrame(titlePlaceholderFrame)
  titlePlaceholderFrame = window.requestAnimationFrame(decorateTitlePlaceholder)
}

function ensureRequiredTitleHeading(): boolean {
  if (!props.ensureTitleHeading || props.disabled || !editorReady || !editor) return false
  const root = editorContentRoot()
  if (!root) return false
  let heading = root.firstElementChild as HTMLElement | null
  let restored = false
  if (heading?.tagName !== 'H1') {
    const template = document.createElement('template')
    template.innerHTML = editor.vditor.lute.Md2VditorIRDOM('# ')
    heading = template.content.querySelector<HTMLElement>('h1')
    if (!heading) return false
    root.prepend(heading)
    editor.vditor.undo?.addToUndoStack(editor.vditor)
    restored = true
  }
  if (!titleHeadingIsEmpty(heading)) return restored
  const caretAnchor = ensureTitleCaretAnchor(heading)
  // Vditor 删除空标题块时会把光标留在下方正文。补回 H1 后将光标放到标题末尾，
  // 否则用户看到了标题占位，但输入仍会落到下一个段落。
  root.focus({ preventScroll: true })
  const range = document.createRange()
  range.setStart(caretAnchor, caretAnchor.data.length)
  range.collapse(true)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)
  return restored
}

function handleEditorInput(event: Event) {
  const root = editorContentRoot()
  const target = event.target
  if (!root || !(target instanceof Node) || !root.contains(target)) return
  window.cancelAnimationFrame(titleHeadingFrame)
  titleHeadingFrame = window.requestAnimationFrame(() => {
    if (ensureRequiredTitleHeading()) syncFromEditor()
    queueTitlePlaceholderDecoration()
  })
}

function applyTextColor(item: TextColorItem) {
  if (!editor || props.disabled || !restoreSavedSelection()) return
  const selection = window.getSelection()
  if (!selection?.rangeCount) return
  const range = selection.getRangeAt(0)
  const startBlock = selectionBlock(range.startContainer)
  const endBlock = selectionBlock(range.endContainer)
  const startElement =
    range.startContainer instanceof Element
      ? range.startContainer
      : range.startContainer.parentElement
  if (
    range.collapsed ||
    !startBlock ||
    startBlock !== endBlock ||
    startBlock.tagName === 'PRE' ||
    startElement?.closest('code, .vditor-ir__marker')
  ) {
    editor.tip(t('components.markdown.textColorInlineOnly'))
    colorMenuOpen.value = false
    return
  }

  const wrapper = document.createElement('span')
  wrapper.className = `markdown-text-color is-${item.key}`
  wrapper.dataset.goteamsColor = item.value
  wrapper.appendChild(range.extractContents())
  const opening = inlineMarker(
    `<span style="color: ${item.value}" data-goteams-color="${item.value}">`,
  )
  const closing = inlineMarker('</span>')
  range.insertNode(closing)
  range.insertNode(wrapper)
  range.insertNode(opening)

  const nextRange = document.createRange()
  nextRange.selectNodeContents(wrapper)
  selection.removeAllRanges()
  selection.addRange(nextRange)
  savedSelectionRange = nextRange.cloneRange()
  selectedTextColor.value = item.value
  colorMenuOpen.value = false
  typeMenuOpen.value = false
  editor.vditor.undo?.addToUndoStack(editor.vditor)
  syncFromEditor()
  void refreshFloatingToolbar()
}

function blockMarkdown(block: HTMLElement) {
  if (!editor) return ''
  const container = document.createElement('div')
  container.appendChild(block.cloneNode(true))
  return editor.vditor.lute.VditorIRDOM2Md(container.innerHTML).trim()
}

type BlockLineTarget = {
  block: HTMLElement
  beforeMarkdown: string
  lineMarkdown: string
  afterMarkdown: string
}

function nodePathWithin(root: Node, node: Node): number[] | null {
  const path: number[] = []
  let current: Node | null = node
  while (current && current !== root) {
    const parent: Node | null = current.parentNode
    if (!parent) return null
    const index = Array.prototype.indexOf.call(parent.childNodes, current) as number
    if (index < 0) return null
    path.unshift(index)
    current = parent
  }
  return current === root ? path : null
}

function nodeAtPath(root: Node, path: number[]) {
  let current: Node | null = root
  for (const index of path) {
    current = current?.childNodes[index] ?? null
    if (!current) return null
  }
  return current
}

function cloneBlockSlice(
  block: HTMLElement,
  start: { node: Node; offset: number } | null,
  end: { node: Node; offset: number } | null,
) {
  const startPath = start ? nodePathWithin(block, start.node) : null
  const endPath = end ? nodePathWithin(block, end.node) : null
  if ((start && !startPath) || (end && !endPath)) return null

  const clone = block.cloneNode(true) as HTMLElement
  if (end && endPath) {
    const endNode = nodeAtPath(clone, endPath)
    if (!endNode) return null
    const trailingRange = document.createRange()
    trailingRange.selectNodeContents(clone)
    trailingRange.setStart(endNode, end.offset)
    trailingRange.deleteContents()
  }
  if (start && startPath) {
    const startNode = nodeAtPath(clone, startPath)
    if (!startNode) return null
    const leadingRange = document.createRange()
    leadingRange.selectNodeContents(clone)
    leadingRange.setEnd(startNode, start.offset)
    leadingRange.deleteContents()
  }
  return clone
}

function activeBlockLineTarget(): BlockLineTarget | null {
  const block = activeBlockElement
  const lineRange = activeLineRange
  if (
    !block ||
    !lineRange ||
    !block.isConnected ||
    !block.contains(lineRange.startContainer) ||
    !block.contains(lineRange.endContainer)
  ) {
    return null
  }

  const start = { node: lineRange.startContainer, offset: lineRange.startOffset }
  const end = { node: lineRange.endContainer, offset: lineRange.endOffset }
  const beforeBlock = cloneBlockSlice(block, null, start)
  const lineBlock = cloneBlockSlice(block, start, end)
  const afterBlock = cloneBlockSlice(block, end, null)
  if (!beforeBlock || !lineBlock || !afterBlock) return null

  const lineMarkdown = blockMarkdown(lineBlock)
  if (!lineMarkdown) return null
  return {
    block,
    beforeMarkdown: blockMarkdown(beforeBlock),
    lineMarkdown,
    afterMarkdown: blockMarkdown(afterBlock),
  }
}

function replaceBlockLine(target: BlockLineTarget, replacement: string) {
  const nextBlockMarkdown = [target.beforeMarkdown, replacement, target.afterMarkdown]
    .filter((part) => part.trim())
    .join('\n\n')
  return replaceBlockMarkdown(target.block, nextBlockMarkdown)
}

function locateBlockMarkdown(block: HTMLElement) {
  const root = editorContentRoot()
  if (!editor || !root) return null
  const source = serializedEditorValue()
  let cursor = 0
  for (const child of Array.from(root.children)) {
    if (!(child instanceof HTMLElement)) continue
    const markdown = blockMarkdown(child)
    if (!markdown) continue
    let start = source.indexOf(markdown, cursor)
    if (start < 0) start = source.indexOf(markdown)
    if (child === block) {
      return start < 0 ? null : { source, markdown, start, end: start + markdown.length }
    }
    if (start >= 0) cursor = start + markdown.length
  }
  return null
}

function replaceBlockMarkdown(block: HTMLElement, replacement: string) {
  const located = locateBlockMarkdown(block)
  if (!editor || !located) {
    editor?.tip(t('components.markdown.blockOperationFailed'))
    return false
  }
  const scroller = hostRef.value?.querySelector<HTMLElement>('.vditor-ir')
  const scrollTop = scroller?.scrollTop ?? 0
  const nextValue =
    located.source.slice(0, located.start) + replacement + located.source.slice(located.end)
  applyingExternalValue = true
  editor.setValue(nextValue, false)
  applyingExternalValue = false
  emit('update:modelValue', serializedEditorValue())
  blockMenuOpen.value = false
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  hideBlockHandle()
  floatingVisible.value = false
  savedSelectionRange = null
  activeLineRange = null
  updateCurrentBlock(null)
  void nextTick(() => {
    if (scroller) scroller.scrollTop = scrollTop
    queueTextColorDecoration()
  })
  return true
}

function stripBlockSyntax(markdown: string) {
  let value = markdown.trim()
  const fenced = value.match(/^```[^\n]*\n([\s\S]*?)\n```$/)
  if (fenced) value = fenced[1]
  return value
    .split('\n')
    .map((line) =>
      line
        .replace(/^\s*>\s?/, '')
        .replace(/^\s*#{1,6}\s+/, '')
        .replace(/^\s*[-+*]\s+\[[ xX]\]\s+/, '')
        .replace(/^\s*[-+*]\s+/, '')
        .replace(/^\s*\d+[.)]\s+/, '')
        .replace(/^==([\s\S]*)==$/, '$1'),
    )
    .join('\n')
    .trim()
}

function transformBlockMarkdown(markdown: string, type: BlockTypeKey) {
  const content = stripBlockSyntax(markdown)
  const lines = content.split('\n')
  switch (type) {
    case 'text':
      return content
    case 'heading1':
    case 'heading2':
    case 'heading3':
    case 'heading4':
    case 'heading5':
    case 'heading6': {
      const level = Number(type.slice(-1))
      return `${'#'.repeat(level)} ${lines.join(' ').trim()}`
    }
    case 'unorderedList':
      return lines.map((line) => `- ${line}`).join('\n')
    case 'orderedList':
      return lines.map((line, index) => `${index + 1}. ${line}`).join('\n')
    case 'checkList':
      return lines.map((line) => `- [ ] ${line}`).join('\n')
    case 'highlightBlock':
      return `==${lines.join(' ').trim()}==`
    case 'quote':
      return lines.map((line) => `> ${line}`).join('\n')
    case 'codeBlock':
      return `\`\`\`\n${content}\n\`\`\``
  }
}

function applyBlockType(type: BlockTypeKey) {
  if (props.disabled) return
  restoreSavedSelection()
  const lineTarget = activeBlockLineTarget()
  if (lineTarget) {
    replaceBlockLine(lineTarget, transformBlockMarkdown(lineTarget.lineMarkdown, type))
    return
  }
  const block =
    activeBlockElement ||
    (savedSelectionRange ? selectionBlock(savedSelectionRange.startContainer) : null)
  if (!block || block === editorContentRoot()) return
  const markdown = blockMarkdown(block)
  replaceBlockMarkdown(block, transformBlockMarkdown(markdown, type))
}

async function copyText(value: string) {
  try {
    await navigator.clipboard.writeText(value)
    return true
  } catch {
    const textarea = document.createElement('textarea')
    textarea.value = value
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    const copied = document.execCommand('copy')
    textarea.remove()
    return copied
  }
}

async function performBlockAction(action: BlockActionKey) {
  if (props.disabled || !activeBlockElement) return
  const block = activeBlockElement
  const lineTarget = activeBlockLineTarget()
  const markdown = lineTarget?.lineMarkdown || blockMarkdown(block)
  if (!markdown) return
  if (action === 'copy' || action === 'cut') {
    const copied = await copyText(markdown)
    editor?.tip(t(copied ? 'components.markdown.copied' : 'components.markdown.copyFailed'), 1600)
    if (!copied || action === 'copy') {
      blockMenuOpen.value = false
      return
    }
  }
  if (action === 'duplicate') {
    if (lineTarget) replaceBlockLine(lineTarget, `${markdown}\n\n${markdown}`)
    else replaceBlockMarkdown(block, `${markdown}\n\n${markdown}`)
  } else if (action === 'cut') {
    if (lineTarget) replaceBlockLine(lineTarget, '')
    else replaceBlockMarkdown(block, '')
  } else if (action === 'delete') {
    blockMenuOpen.value = false
    Modal.confirm({
      title: t('components.markdown.deleteBlockTitle'),
      content: t('components.markdown.deleteBlockDescription'),
      okText: t('components.markdown.delete'),
      okType: 'danger',
      cancelText: t('components.markdown.cancel'),
      onOk: () => {
        if (lineTarget) replaceBlockLine(lineTarget, '')
        else replaceBlockMarkdown(block, '')
      },
    })
  }
}

async function openLinkDialog() {
  const selectionText = savedSelectionRange?.toString() || currentSelection()
  if (!selectionText) return
  linkText.value = selectionText
  linkUrl.value = ''
  linkDialogOpen.value = true
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  await nextTick()
  const host = rootRef.value
  const toolbar = floatingRef.value
  const dialog = linkDialogRef.value
  if (host && toolbar && dialog) {
    const maxLeft = Math.max(FLOATING_GAP, host.clientWidth - dialog.offsetWidth - FLOATING_GAP)
    let left = Math.min(Math.max(toolbar.offsetLeft, FLOATING_GAP), maxLeft)
    let top = toolbar.offsetTop + toolbar.offsetHeight + FLOATING_GAP
    if (top + dialog.offsetHeight > host.clientHeight - FLOATING_GAP) {
      top = Math.max(FLOATING_GAP, toolbar.offsetTop - dialog.offsetHeight - FLOATING_GAP)
    }
    linkDialogStyle.value = { left: `${Math.round(left)}px`, top: `${Math.round(top)}px` }
  }
  linkUrlInputRef.value?.focus()
}

function closeLinkDialog() {
  linkDialogOpen.value = false
  linkText.value = ''
  linkUrl.value = ''
}

function confirmLink() {
  const text = linkText.value.trim()
  const url = linkUrl.value.trim()
  if (!editor || !text || !url) return
  if (/^(?:javascript|data|vbscript):/i.test(url)) {
    editor.tip(t('components.markdown.linkUrlInvalid'))
    return
  }
  if (!restoreSavedSelection()) return
  const escapedText = text.replace(/([[\]])/g, '\\$1')
  const escapedUrl = url.replace(/\s/g, '%20').replace(/\)/g, '%29')
  editor.updateValue(`[${escapedText}](${escapedUrl})`)
  syncFromEditor()
  closeLinkDialog()
  void refreshFloatingToolbar()
}

function handleDocumentPointerDown(event: PointerEvent) {
  const root = rootRef.value
  const target = event.target
  if (!root || !(target instanceof Node)) return
  if (!root.contains(target)) {
    closeLinkDialog()
    hideEditorOverlays()
    return
  }
  if (!(target instanceof Element)) return
  if (
    target.closest('.floating-toolbar') ||
    target.closest('.block-handle') ||
    target.closest('.link-dialog')
  ) {
    return
  }
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  blockMenuOpen.value = false
  if (linkDialogOpen.value) closeLinkDialog()
}

function handleDocumentKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  blockMenuOpen.value = false
  if (linkDialogOpen.value) closeLinkDialog()
}

/* ---------------- 生命周期 ---------------- */

onMounted(() => {
  void nextTick(() => createEditor())
  rootRef.value?.addEventListener('scroll', handleEditorScroll, true)
  rootRef.value?.addEventListener('input', handleEditorInput)
  rootRef.value?.addEventListener('keydown', handleTextColorDelete, true)
  // S-UI-11: 仅在启用浮动工具栏时注册；S-UI-12: 不注册任何 contextmenu 处理器
  if (props.floatingToolbar) {
    document.addEventListener('selectionchange', handleDocumentSelectionChange)
    document.addEventListener('pointerdown', handleDocumentPointerDown)
    document.addEventListener('keydown', handleDocumentKeydown)
  }
})

watch(
  () => props.modelValue,
  (value) => {
    if (!editorReady || !editor) return
    const nextValue = editorInputValue(value)
    if (nextValue === serializedEditorValue()) return
    applyingExternalValue = true
    editor.setValue(nextValue, true)
    applyingExternalValue = false
    observeTextColorMarkers()
    queueTextColorDecoration()
    queueTitlePlaceholderDecoration()
  },
)

watch(
  () => props.titlePlaceholder,
  () => queueTitlePlaceholderDecoration(),
)

watch(
  () => props.disabled,
  (disabled) => {
    if (!editorReady || !editor) return
    if (disabled) {
      editor.disabled()
      hideEditorOverlays()
    } else editor.enable()
  },
)

watch(locale, async () => {
  syncFromEditor()
  hideFloatingToolbar()
  stopTextColorMarkerObserver()
  editorVersion += 1
  const version = editorVersion
  editorReady = false
  editor?.destroy()
  editor = undefined
  await nextTick()
  if (!destroyed && version === editorVersion) createEditor()
})

onBeforeUnmount(() => {
  destroyed = true
  editorVersion += 1
  editorReady = false
  // web/AGENTS.md：组件卸载必须清理监听器
  document.removeEventListener('selectionchange', handleDocumentSelectionChange)
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
  document.removeEventListener('keydown', handleDocumentKeydown)
  rootRef.value?.removeEventListener('scroll', handleEditorScroll, true)
  rootRef.value?.removeEventListener('input', handleEditorInput)
  rootRef.value?.removeEventListener('keydown', handleTextColorDelete, true)
  stopTextColorMarkerObserver()
  window.cancelAnimationFrame(textColorDecorationFrame)
  window.cancelAnimationFrame(titlePlaceholderFrame)
  window.cancelAnimationFrame(titleHeadingFrame)
  editor?.destroy()
  editor = undefined
})

defineExpose({
  getValue: currentValue,
  htmlToMarkdown(value: string) {
    return editorReady && editor ? editor.html2md(value) : value
  },
  focus() {
    editor?.focus()
  },
  resize() {
    const host = hostRef.value
    const vditorEl = host?.querySelector<HTMLElement>('.vditor')
    if (host && vditorEl) vditorEl.style.height = '100%'
  },
  /** S-UI-11: 当前选区信息（含前后文定位片段与选区矩形） */
  getSelectionInfo(): EditorSelectionInfo | null {
    return readEditorSelection()
  },
  /** S-UI-10: 大纲跳转——定位第 index 个标题元素并滚动到可视区 */
  scrollToHeading(index: number) {
    const host = hostRef.value
    if (!host || index < 0) return
    const scroller = host.querySelector<HTMLElement>('.vditor-ir .vditor-reset')
    if (!scroller) return
    const headings = host.querySelectorAll<HTMLElement>(
      '.vditor-ir .vditor-reset h1, .vditor-ir .vditor-reset h2, .vditor-ir .vditor-reset h3, ' +
        '.vditor-ir .vditor-reset h4, .vditor-ir .vditor-reset h5, .vditor-ir .vditor-reset h6',
    )
    const heading = headings[index]
    if (!heading) return
    const scrollerRect = scroller.getBoundingClientRect()
    const headingRect = heading.getBoundingClientRect()
    const top = scroller.scrollTop + headingRect.top - scrollerRect.top
    scroller.scrollTo({ top: Math.max(0, top), behavior: 'smooth' })
  },
})
</script>

<style scoped>
.markdown-editor {
  position: relative;
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  overflow: hidden;
}

.markdown-editor-host {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.markdown-editor :deep(.vditor) {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.markdown-editor :deep(.vditor-content),
.markdown-editor :deep(.vditor-ir) {
  min-height: 0;
  flex: 1;
}

.markdown-editor.is-disabled :deep(.vditor-toolbar) {
  pointer-events: none;
}

.markdown-editor.is-toolbar-hidden :deep(.vditor-toolbar) {
  display: none;
}

.floating-toolbar {
  position: absolute;
  z-index: 20;
  display: flex;
  max-width: calc(100% - 16px);
  align-items: center;
  gap: 2px;
  padding: 4px 6px;
  border: 1px solid #f0f0f0;
  border-radius: 12px;
  opacity: 0;
  background: #fff;
  box-shadow:
    0 6px 30px 5px rgba(0, 0, 0, 0.05),
    0 16px 24px 2px rgba(0, 0, 0, 0.04),
    0 8px 10px -5px rgba(0, 0, 0, 0.08);
  transition: opacity 0.12s ease;
}

.floating-toolbar.is-ready {
  opacity: 1;
}

.floating-tool {
  display: inline-flex;
  width: 28px;
  height: 28px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 8px;
  color: #595959;
  background: transparent;
  cursor: pointer;
  font-size: 15px;
  transition:
    color 0.18s ease,
    background 0.18s ease;
}

.floating-menu-anchor {
  position: relative;
  display: flex;
}

.floating-type-trigger {
  width: auto;
  min-width: 42px;
  gap: 4px;
  padding: 0 7px;
  font-size: 14px;
}

.floating-type-label {
  display: inline-flex;
  min-width: 16px;
  align-items: center;
  justify-content: center;
}

.floating-type-chevron {
  color: #8c8c8c;
  font-size: 8px;
}

.floating-color-trigger {
  width: 42px;
  gap: 4px;
}

.floating-color-letter {
  color: #262626;
  font-size: 15px;
  font-weight: 500;
  line-height: 1;
  text-decoration: none;
}

.floating-tool:hover {
  color: #3157e2;
  background: #f0f1f3;
}

.floating-tool:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.floating-divider {
  width: 1px;
  height: 18px;
  margin: 0 4px;
  background: #e2e4e8;
}

.floating-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}

.floating-popover {
  position: absolute;
  z-index: 24;
  padding: 10px;
  border: 1px solid rgba(28, 29, 32, 0.1);
  border-radius: 11px;
  background: rgba(255, 255, 255, 0.98);
  box-shadow:
    0 12px 30px rgba(15, 23, 42, 0.14),
    0 2px 6px rgba(15, 23, 42, 0.07);
}

.text-color-menu {
  top: 36px;
  right: -4px;
  display: flex;
  gap: 6px;
  padding: 8px;
}

.text-color-item {
  display: inline-flex;
  width: 30px;
  height: 30px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 1px solid transparent;
  border-radius: 7px;
  background: transparent;
  cursor: pointer;
}

.text-color-item:hover,
.text-color-item:focus-visible {
  border-color: #d9d9d9;
  outline: none;
  background: #f5f6f8;
}

.text-color-item.is-selected {
  border-color: #3157e2;
  box-shadow: 0 0 0 2px rgba(49, 87, 226, 0.08);
}

.text-color-swatch {
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border-radius: 5px;
  font-size: 15px;
  font-weight: 500;
  line-height: 1;
  text-decoration: none;
}

.text-color-swatch.is-neutral {
  color: #262626;
  background: #f5f5f5;
}

.text-color-swatch.is-success {
  color: #16a34a;
  background: #f0fdf4;
}

.text-color-swatch.is-warning {
  color: #ed744a;
  background: #fff5e5;
}

.text-color-swatch.is-error {
  color: #fb363f;
  background: #fef2f2;
}

.text-color-swatch.is-primary {
  color: #3157e2;
  background: #e5efff;
}

.markdown-editor :deep(.markdown-text-color.is-neutral) {
  color: #262626;
}

.markdown-editor :deep(.markdown-text-color.is-success) {
  color: #16a34a;
}

.markdown-editor :deep(.markdown-text-color.is-warning) {
  color: #ed744a;
}

.markdown-editor :deep(.markdown-text-color.is-error) {
  color: #fb363f;
}

.markdown-editor :deep(.markdown-text-color.is-primary) {
  color: #3157e2;
}

.markdown-editor :deep(.vditor-ir .vditor-reset > h1[data-goteams-title-placeholder]::after) {
  content: attr(data-goteams-title-placeholder);
  color: #bfbfbf;
  font-weight: 400;
  pointer-events: none;
}

.block-type-menu {
  top: 36px;
  left: -96px;
  display: flex;
  width: 130px;
  flex-direction: column;
  gap: 2px;
}

.block-type-menu-item,
.block-menu-action {
  display: flex;
  width: 100%;
  height: 30px;
  align-items: center;
  gap: 8px;
  padding: 0 7px;
  border: 0;
  border-radius: 7px;
  color: #262626;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
  text-align: left;
}

.block-type-menu-item:hover,
.block-type-menu-item:focus-visible,
.block-menu-action:hover,
.block-menu-action:focus-visible,
.block-type-grid-item:hover,
.block-type-grid-item:focus-visible {
  outline: none;
  background: #f5f5f5;
}

.block-type-menu-icon {
  display: inline-flex;
  width: 22px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  color: #595959;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 13px;
  text-align: center;
}

.block-type-icon {
  color: #262626;
  font-size: 16px;
}

.block-type-image-icon {
  display: block;
  width: 16px;
  height: 16px;
  flex: 0 0 auto;
}

.block-handle {
  position: absolute;
  z-index: 18;
  opacity: 0;
  transition: opacity 0.12s ease;
}

.block-handle.is-ready {
  opacity: 1;
}

.block-handle-trigger {
  display: flex;
  height: 28px;
  align-items: center;
  gap: 4px;
  padding: 0 6px;
  border: 1px solid #f0f0f0;
  border-radius: 8px;
  color: #595959;
  background: #fff;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.08);
  cursor: pointer;
  font-size: 13px;
}

.block-handle-trigger:hover,
.block-handle-trigger:focus-visible {
  outline: 2px solid rgba(49, 87, 226, 0.15);
  outline-offset: 1px;
}

.block-handle-type {
  display: inline-flex;
  min-width: 14px;
  align-items: center;
  justify-content: center;
  text-align: center;
}

.block-menu {
  top: 36px;
  left: 0;
  display: flex;
  width: 200px;
  flex-direction: column;
  gap: 3px;
}

.block-type-grid {
  display: grid;
  grid-template-columns: repeat(6, 24px);
  gap: 6px;
}

.block-type-grid-item {
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 7px;
  color: #262626;
  background: transparent;
  cursor: pointer;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 11px;
}

.block-menu-divider {
  width: 100%;
  height: 1px;
  margin: 3px 0;
  background: #e7e9ec;
}

.block-menu-action :deep(svg) {
  width: 16px;
  height: 16px;
}

.block-menu-action-danger {
  color: #fb363f;
}

.link-dialog {
  position: absolute;
  z-index: 30;
  display: flex;
  width: 336px;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  border: 1px solid rgba(28, 29, 32, 0.1);
  border-radius: 11px;
  background: #fff;
  box-shadow:
    0 12px 30px rgba(15, 23, 42, 0.14),
    0 2px 6px rgba(15, 23, 42, 0.07);
}

.link-dialog-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: #262626;
  font-size: 14px;
  line-height: 22px;
}

.link-dialog-close {
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #8c8c8c;
  background: transparent;
  cursor: pointer;
  font-size: 18px;
}

.link-dialog-close:hover {
  color: #262626;
  background: #f5f5f5;
}

.link-dialog-field {
  display: grid;
  grid-template-columns: 60px minmax(0, 1fr);
  align-items: center;
  gap: 8px;
  color: #595959;
  font-size: 14px;
}

.link-dialog-field input {
  width: 100%;
  height: 30px;
  min-width: 0;
  padding: 0 10px;
  border: 1px solid #d9d9d9;
  border-radius: 7px;
  color: #262626;
  background: #fff;
  outline: none;
}

.link-dialog-field input:focus {
  border-color: #3157e2;
  box-shadow: 0 0 0 2px rgba(49, 87, 226, 0.15);
}

.link-dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.link-dialog-button {
  height: 30px;
  padding: 0 14px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  color: #595959;
  background: #fff;
  cursor: pointer;
}

.link-dialog-button-primary {
  border-color: #3157e2;
  color: #fff;
  background: #3157e2;
}

.link-dialog-button:disabled {
  border-color: #d9d9d9;
  color: #bfbfbf;
  background: #f5f5f5;
  cursor: not-allowed;
}

@media (prefers-reduced-motion: reduce) {
  .floating-toolbar,
  .floating-tool,
  .block-handle {
    transition: none;
  }
}
</style>
