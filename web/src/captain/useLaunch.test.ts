import { act, renderHook, waitFor } from '@testing-library/react'
import type { Detail } from '../api'
import { useLaunch } from './useLaunch'

const detail = (over: Partial<Detail> = {}): Detail => ({
  id: 'l1', status: 'draft', source: 'github', title: 'one', summary: '', releaseTag: '', releaseUrl: '',
  launchAt: null, windowMinutes: 90, crewCap: 200, openCount: 0, openUrl: '', showUrl: '', pngUrl: '', roles: [],
  bufferConnected: true, headlines: [], chores: [], captainDrafts: [], crew: [], checks: [], deadLetters: [],
  counts: { in: 0, total: 0 }, captainName: '', qrLogo: 'plane', qrLogoUrl: '', images: [], includeCard: true, ...over,
})

const ok = (b: unknown) => new Response(JSON.stringify(b), { status: 200 })

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

test('polls every 15 s only once launched; a draft is fetched once', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const fetchMock = vi.fn(() => Promise.resolve(ok(detail())))
  vi.stubGlobal('fetch', fetchMock)
  const { result, rerender } = renderHook(() => useLaunch('l1'))
  await waitFor(() => expect(result.current.detail?.title).toBe('one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(40_000) })
  expect(fetchMock).toHaveBeenCalledTimes(1)

  fetchMock.mockImplementation(() => Promise.resolve(ok(detail({ status: 'launched' }))))
  await act(async () => { await result.current.reload() })
  rerender()
  expect(result.current.detail?.status).toBe('launched')
  const n = fetchMock.mock.calls.length
  await act(async () => { await vi.advanceTimersByTimeAsync(15_500) })
  expect(fetchMock).toHaveBeenCalledTimes(n + 1)
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
  expect(fetchMock).toHaveBeenCalledTimes(n + 2)
})

test('a slow answer for the previous id never overwrites the current launch', async () => {
  let releaseOld!: (r: Response) => void
  const fetchMock = vi.fn((url: string) => {
    if (url.endsWith('/l1')) return new Promise<Response>((r) => { releaseOld = r })
    return Promise.resolve(ok(detail({ id: 'l2', title: 'two' })))
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result, rerender } = renderHook(({ id }) => useLaunch(id), { initialProps: { id: 'l1' } })
  rerender({ id: 'l2' })
  await waitFor(() => expect(result.current.detail?.title).toBe('two'))
  await act(async () => {
    releaseOld(ok(detail({ id: 'l1', title: 'one' })))
    await Promise.resolve()
  })
  expect(result.current.detail?.title).toBe('two')
  expect(result.current.detail?.id).toBe('l2')
})

test('an error for the current id is kept, one for a previous id is dropped', async () => {
  let failOld!: (r: Response) => void
  const fetchMock = vi.fn((url: string) => {
    if (url.endsWith('/l1')) return new Promise<Response>((r) => { failOld = r })
    return Promise.resolve(ok(detail({ id: 'l2', title: 'two' })))
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result, rerender } = renderHook(({ id }) => useLaunch(id), { initialProps: { id: 'l1' } })
  rerender({ id: 'l2' })
  await waitFor(() => expect(result.current.detail?.title).toBe('two'))
  await act(async () => {
    failOld(new Response(JSON.stringify({ error: 'not_found' }), { status: 404 }))
    await Promise.resolve()
  })
  expect(result.current.error).toBeUndefined()
})
