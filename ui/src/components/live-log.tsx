import { useEffect, useRef, useState } from 'react'
import { ChevronDownIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { isViewportAtBottom } from '@/lib/scroll'

/**
 * Live log panel for one run. Polls every 2 s and pins the viewport to the
 * newest line while following. Scrolling up pauses the pin so earlier output
 * can be inspected; returning to the bottom or pressing the scroll-to-bottom
 * button resumes it.
 */
export function LiveLog({ runId }: { runId: string }) {
  const [logs, setLogs] = useState('')
  const [following, setFollowing] = useState(true)
  const logRef = useRef<HTMLDivElement>(null)
  const followingRef = useRef(true)
  // set while a programmatic pin-to-bottom is in flight so the scroll events
  // it causes never overwrite the user's follow state
  const autoScrollingRef = useRef(false)

  function pinToBottom() {
    autoScrollingRef.current = true
    requestAnimationFrame(() => {
      const viewport = logRef.current
      if (viewport) viewport.scrollTop = viewport.scrollHeight
      autoScrollingRef.current = false
    })
  }

  // the consumer keys this component by run id, so log state starts fresh per run
  useEffect(() => {
    let alive = true
    const load = async () => {
      try {
        const data = await api<{ logs: string }>(`runs/${runId}/logs`)
        if (!alive) return
        setLogs(data.logs)
        if (followingRef.current) pinToBottom()
      } catch {
        /* the run may have been cleaned up between polls */
      }
    }
    load()
    const timer = setInterval(load, 2000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [runId])

  function resumeFollow() {
    followingRef.current = true
    setFollowing(true)
    pinToBottom()
  }

  function handleScroll() {
    if (autoScrollingRef.current) return
    const viewport = logRef.current
    if (!viewport) return
    const atBottom = isViewportAtBottom(viewport)
    if (atBottom === followingRef.current) return
    followingRef.current = atBottom
    setFollowing(atBottom)
  }

  return (
    <div className="panel lg:col-span-2">
      <LogPanelHead following={following} onResume={resumeFollow} />
      <div className="panel-body">
        <div ref={logRef} onScroll={handleScroll} className="console h-[380px]">
          <pre className="break-all whitespace-pre-wrap">{logs || 'Waiting for output…'}</pre>
        </div>
      </div>
    </div>
  )
}

export function LogPanelHead({ following, onResume }: { following: boolean; onResume: () => void }) {
  return (
    <div className="panel-head">
      <span className="panel-title">Live log</span>
      {following ? (
        <span className="panel-meta">last 128 KiB · polls every 2 s</span>
      ) : (
        <>
          <span className="panel-meta">auto-scroll paused · new lines below</span>
          <Button variant="secondary" size="sm" onClick={onResume}>
            <ChevronDownIcon /> Scroll to bottom
          </Button>
        </>
      )}
    </div>
  )
}
