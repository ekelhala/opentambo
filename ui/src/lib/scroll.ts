/**
 * Pixels of distance from the bottom within which a scrollable viewport still
 * counts as "at bottom". Absorbs sub-pixel scroll rounding so a tiny drag
 * does not silently drop follow mode.
 */
export const BOTTOM_SLACK = 8

type ScrollMetrics = { scrollHeight: number; scrollTop: number; clientHeight: number }

/** Pure bottom-detection so follow logic is testable without a DOM. */
export function isViewportAtBottom(el: ScrollMetrics, slack = BOTTOM_SLACK): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= slack
}
