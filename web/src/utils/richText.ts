const LEGACY_RICH_TEXT_ROOT_RE =
  /^\s*<(?:article|blockquote|center|div|figure|figcaption|font|h[1-6]|img|ol|p|pre|section|table|ul)\b/i

/**
 * 仅识别以块级富文本节点开头的历史任务描述。
 *
 * Markdown 代码围栏、普通正文，以及正文中夹杂的内联 HTML 不应被整体送入
 * HTML-to-Markdown 转换，否则用户只编辑其他字段也可能导致描述被重写。
 */
export function isLegacyRichTextHtml(value?: string) {
  return LEGACY_RICH_TEXT_ROOT_RE.test(value || '')
}
