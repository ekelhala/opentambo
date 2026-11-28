import { expect, test } from 'bun:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { LogPanelHead } from '@/components/live-log'
import { BOTTOM_SLACK, isViewportAtBottom } from '@/lib/scroll'

// The live log must stop auto-scrolling when the operator scrolls up to read
// earlier output, and resume at the bottom or via an explicit button.
// These tests pin the pure bottom-detection and both header states; the
// wiring between them lives in live-log.tsx.

const viewport = (scrollHeight: number, scrollTop: number, clientHeight: number) => ({
  scrollHeight,
  scrollTop,
  clientHeight,
})

test('at the bottom counts as at bottom', () => {
  expect(isViewportAtBottom(viewport(1000, 900, 100))).toBe(true)
})

test('fractional scroll rounding still counts as at bottom', () => {
  expect(isViewportAtBottom(viewport(1000, 900 - BOTTOM_SLACK, 100))).toBe(true)
})

test('scrolled up past the slack leaves follow mode', () => {
  expect(isViewportAtBottom(viewport(1000, 900 - BOTTOM_SLACK - 1, 100))).toBe(false)
})

test('short logs without a scrollbar are at bottom', () => {
  expect(isViewportAtBottom(viewport(80, 0, 380))).toBe(true)
})

test('header shows polling meta and no pause notice while following', () => {
  const html = renderToStaticMarkup(<LogPanelHead following />)
  expect(html).toContain('Live log')
  expect(html).toContain('refreshes every 2 seconds')
  expect(html).not.toContain('scrolling paused')
})

test('header announces paused auto-scroll without a button', () => {
  const html = renderToStaticMarkup(<LogPanelHead following={false} />)
  expect(html).toContain('scrolling paused')
  expect(html).not.toContain('Scroll to bottom')
})
