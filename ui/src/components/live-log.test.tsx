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

test('header shows polling meta and no button while following', () => {
  const html = renderToStaticMarkup(<LogPanelHead following onResume={() => {}} />)
  expect(html).toContain('Live log')
  expect(html).toContain('polls every 2 s')
  expect(html).not.toContain('Scroll to bottom')
})

test('header announces paused auto-scroll and offers a way back down', () => {
  const html = renderToStaticMarkup(<LogPanelHead following={false} onResume={() => {}} />)
  expect(html).toContain('auto-scroll paused')
  expect(html).toContain('Scroll to bottom')
  expect(html).toContain('<button')
})
