import { useEffect, useState } from 'react'

export type View = 'dashboard' | 'servers' | 'server' | 'downloads' | 'files'

export interface Route {
  view: View
  /** Run id for the server detail view. */
  id?: string
}

const VIEWS: View[] = ['dashboard', 'servers', 'server', 'downloads', 'files']

function parse(): Route {
  const parts = window.location.hash.replace(/^#\/?/, '').split('/')
  if (parts[0] === 'server' && parts[1]) return { view: 'server', id: parts[1] }
  if (VIEWS.includes(parts[0] as View) && parts[0] !== 'server') return { view: parts[0] as View }
  return { view: 'dashboard' }
}

export function useHashRoute(): Route {
  const [route, setRoute] = useState<Route>(parse)
  useEffect(() => {
    const onChange = () => setRoute(parse())
    window.addEventListener('hashchange', onChange)
    return () => window.removeEventListener('hashchange', onChange)
  }, [])
  return route
}

export function navigate(view: string): void {
  const next = `#/${view}`
  if (window.location.hash === next) window.dispatchEvent(new HashChangeEvent('hashchange'))
  else window.location.hash = next
}
