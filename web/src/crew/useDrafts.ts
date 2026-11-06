import { useCallback, useEffect, useState } from 'react'
import { getDrafts, type DraftView } from '../api'

const EVERY = 1500
const GIVE_UP = 20000

const sig = (ds: DraftView[]) => ds.map((d) => `${d.id}:${d.text}`).join('\n')

// Polls for the voiced drafts until voicedReady or 20 s; `polling` is true while waiting.
// `restart(shown)` polls again after a voice change, until the drafts differ from `shown`.
export function useDrafts(memberId: string | undefined, token: string, start: boolean) {
  const [fresh, setFresh] = useState<DraftView[]>()
  const [over, setOver] = useState(false)
  const [round, setRound] = useState({ n: 0, base: '' })

  useEffect(() => {
    if (!memberId || !(start || round.n > 0)) return
    setOver(false)
    let live = true
    let timer: number
    const began = Date.now()
    const tick = async () => {
      let ready = false
      try {
        const r = await getDrafts(memberId, token)
        if (!live) return
        setFresh(r.drafts)
        ready = r.voicedReady && (round.n === 0 || sig(r.drafts) !== round.base)
      } catch {
        // keep the template drafts; try again until the deadline
      }
      if (!live) return
      if (ready || Date.now() - began >= GIVE_UP) setOver(true)
      else timer = window.setTimeout(tick, EVERY)
    }
    timer = window.setTimeout(tick, EVERY)
    return () => {
      live = false
      window.clearTimeout(timer)
    }
  }, [memberId, token, start, round])

  const restart = useCallback((shown: DraftView[]) => setRound((r) => ({ n: r.n + 1, base: sig(shown) })), [])

  // Replaces what is shown with drafts the server just returned (the polled copy would be stale).
  const adopt = useCallback((shown: DraftView[]) => setFresh(shown), [])

  return { fresh, polling: (start || round.n > 0) && !over, restart, adopt }
}
