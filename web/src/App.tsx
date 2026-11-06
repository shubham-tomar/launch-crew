import { useEffect, useRef } from 'react'
import { Route, Routes, useLocation } from 'react-router'
import LaunchDetail from './captain/LaunchDetail'
import Launches from './captain/Launches'
import NewLaunch from './captain/NewLaunch'
import Repos from './captain/Repos'
import Templates from './captain/Templates'
import Approve from './crew/Approve'
import Done from './crew/Done'
import Fallback from './crew/Fallback'
import Invite from './crew/Invite'
import OpenLink from './crew/OpenLink'
import Show from './crew/Show'
import Settings from './settings/Settings'
import Home from './Home'

const reduced = () => typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches

// After navigate() the document does not reload, so nothing tells a screen reader a new
// page is here. Focus the page's main (or its h1) on every pathname change but the first.
function FocusOnNavigate() {
  const { pathname } = useLocation()
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    const target = document.querySelector<HTMLElement>('main') ?? document.querySelector<HTMLElement>('h1')
    if (!target) return
    if (!target.hasAttribute('tabindex')) target.tabIndex = -1
    // Focusing scrolls to the new page's top, as a load would; under reduced motion stay put.
    target.focus({ preventScroll: reduced() })
  }, [pathname])
  return null
}

export default function App() {
  return (
    <>
    <FocusOnNavigate />
    <Routes>
      <Route path="/l/:openToken" element={<OpenLink />} />
      <Route path="/l/:openToken/show" element={<Show />} />
      <Route path="/s/:token" element={<Invite />} />
      <Route path="/s/:token/approve" element={<Approve />} />
      <Route path="/s/:token/done" element={<Done />} />
      <Route path="/s/:token/fallback" element={<Fallback />} />
      <Route path="/settings" element={<Settings />} />
      <Route path="/opt-out" element={<Settings />} />
      <Route path="/captain" element={<Launches />} />
      <Route path="/captain/new" element={<NewLaunch />} />
      <Route path="/captain/launches/:id" element={<LaunchDetail />} />
      <Route path="/captain/repos" element={<Repos />} />
      <Route path="/captain/templates" element={<Templates />} />
      <Route path="*" element={<Home />} />
    </Routes>
    </>
  )
}
