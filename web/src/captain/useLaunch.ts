import { useCallback, useEffect, useRef, useState } from 'react'
import { editCaptainDraft, getLaunch, patchCaptainDraft, patchLaunch, type ApiError, type Detail, type Patch } from '../api'
import { asError } from './format'

const REFETCH = 15000

// Detail loads on mount, after each PATCH and on demand. It polls only once launched:
// on a draft every GET calls Buffer, so a draft is never polled.
export function useLaunch(id: string) {
  const [detail, setDetail] = useState<Detail>()
  const [error, setError] = useState<ApiError>()
  // The id this hook is showing right now. An answer for another id (the page moved on
  // while the request was in flight) or after unmount is dropped, never applied.
  const current = useRef<string | null>(id)
  current.current = id

  useEffect(() => {
    current.current = id
    return () => {
      current.current = null
    }
  }, [id])

  const reload = useCallback(async () => {
    const want = id
    try {
      const d = await getLaunch(want)
      if (current.current === want) {
        setDetail(d)
        setError(undefined)
      }
    } catch (e) {
      if (current.current === want) setError(asError(e))
    }
  }, [id])

  useEffect(() => {
    reload()
  }, [reload])

  const launched = detail?.status === 'launched'
  useEffect(() => {
    if (!launched) return
    const t = window.setInterval(reload, REFETCH)
    return () => window.clearInterval(t)
  }, [launched, reload])

  const patch = useCallback(
    async (p: Patch) => {
      const d = await patchLaunch(id, p)
      if (current.current === id) setDetail(d)
    },
    [id],
  )

  const editDraft = useCallback(
    async (draftId: string, text: string) => {
      const d = await editCaptainDraft(id, draftId, text)
      if (current.current === id) setDetail(d)
    },
    [id],
  )

  // mutate applies a local change to the loaded detail, for optimistic edits
  // whose server answer carries no detail (image add, remove, reorder).
  const mutate = useCallback((fn: (d: Detail) => Detail) => setDetail((d) => (d ? fn(d) : d)), [])

  const skipDraft = useCallback(
    async (draftId: string, skipped: boolean) => {
      const d = await patchCaptainDraft(id, draftId, { skipped })
      if (current.current === id) setDetail(d)
    },
    [id],
  )

  return { detail, error, reload, patch, editDraft, skipDraft, mutate }
}
