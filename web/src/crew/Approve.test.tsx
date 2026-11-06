import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router'
import App from '../App'
import type { ApproveView, Channel, DraftView, Outcome, View } from '../api'

const invite = {
  launch: { id: 'l1', title: 'Ship the thing', project: 'acme', version: 'v1.2.0', releaseUrl: 'https://x/r' },
  member: { id: 'm1', name: 'Ada' },
  card: null, preview: '', verified: true, foreign: false, needsVerify: false, copyText: 'Neutral',
} as unknown as View

const ch = (id: string, name: string, service: string, extra: Partial<Channel> = {}): Channel => ({
  id, name, service, avatar: '', eligible: true, reason: '', checked: true, ...extra,
})
const dr = (id: string, channelId: string, text: string, extra: Partial<DraftView> = {}): DraftView => ({
  id, channelId, network: 'x', text, thread: [], firstComment: '', fromModel: false,
  edited: false, flagged: false, status: 'draft', bufferPostId: '', ...extra,
})

const X = ch('c1', 'Ada on X', 'twitter')
const BSKY = ch('c2', 'ada.bsky.social', 'bluesky')

function approveView(p: Partial<ApproveView> = {}): ApproveView {
  return {
    memberId: 'm1', channels: [X, BSKY],
    drafts: [dr('d1', 'c1', 'template one'), dr('d2', 'c2', 'template two', { network: 'bluesky' })],
    voice: {}, voiceLine: 'Matched to how you write.', timing: ['window', 'queue', 'draft'],
    closed: false, needsReconnect: false, voicedReady: false, ...p,
  }
}

type Mock = { calls: { method: string; url: string; body?: string }[]; polled: { drafts: DraftView[]; voicedReady: boolean } }

function mockApi(opts: {
  connect?: ApproveView | { status: number; error: string; message?: string }
  post?: () => Promise<Response>
  refresh?: () => Promise<Response>
} = {}): Mock {
  const m: Mock = { calls: [], polled: { drafts: [], voicedReady: false } }
  const json = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    m.calls.push({ method, url, body: init?.body as string | undefined })
    if (url === '/api/s/tok1') return json(200, invite)
    if (url.endsWith('/connect')) {
      const c = opts.connect ?? approveView()
      return 'status' in c ? json(c.status, { error: c.error, message: c.message }) : json(200, c)
    }
    if (url.endsWith('/drafts') && method === 'GET') return json(200, m.polled)
    if (url.endsWith('/channels/refresh')) return opts.refresh ? opts.refresh() : json(200, approveView())
    if (url.endsWith('/voice')) return json(200, { voiceLine: 'Short, lowercase.' })
    if (method === 'PUT' || url.endsWith('/disconnect')) return Promise.resolve(new Response(null, { status: 204 }))
    if (url.endsWith('/post')) return opts.post ? opts.post() : json(200, { outcomes: [] })
    return json(404, { error: 'not_found' })
  }))
  return m
}

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>
}

function visit() {
  return render(
    <MemoryRouter initialEntries={['/s/tok1/approve']}>
      <App />
      <Where />
    </MemoryRouter>,
  )
}

const matchMedia = (reduce: boolean) =>
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: reduce && q.includes('reduce'), media: q,
    addEventListener() {}, removeEventListener() {} }))

// Number of visible live-region captions with this text (hidden tab panels don't count).
const caption = (t: string) => screen.queryAllByText(t).filter((e) => e.closest('[hidden]') === null).length
const box = () => screen.findByRole('textbox', { name: 'Your post' })
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
const posts = (m: Mock) => m.calls.filter((c) => c.url.endsWith('/post'))

beforeEach(() => matchMedia(false))
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

test('template then voiced swap keeps user edits', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const m = mockApi()
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  visit()
  expect(await box()).toHaveValue('template one')
  expect(caption('a version in your voice is on its way')).toBe(1)
  await user.click(screen.getByRole('tab', { name: /^ada\.bsky\.social/ }))
  await user.type(await box(), '!')
  await user.click(screen.getByRole('tab', { name: /^Ada on X/ }))
  expect(m.calls.find((c) => c.method === 'PUT')?.url).toBe('/api/crew/m1/drafts/d2')

  m.polled = {
    voicedReady: true,
    drafts: [dr('d1', 'c1', 'voiced one', { fromModel: true }), dr('d2', 'c2', 'voiced two', { fromModel: true, network: 'bluesky' })],
  }
  await advance(1600)
  expect(await box()).toHaveValue('voiced one')
  expect(caption('written how you write')).toBe(1)
  await user.click(screen.getByRole('tab', { name: /^ada\.bsky\.social/ }))
  expect(await box()).toHaveValue('template two!')
  expect(m.calls.some((c) => c.method === 'GET' && c.url.endsWith('/drafts'))).toBe(true)
})

test('swap waits while textarea focused', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const m = mockApi({ connect: approveView({ channels: [X], drafts: [dr('d1', 'c1', 'template one')] }) })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  visit()
  const area = await box()
  await user.click(area)
  m.polled = { voicedReady: true, drafts: [dr('d1', 'c1', 'voiced one', { fromModel: true })] }
  await advance(1600)
  expect(area).toHaveValue('template one')
  await user.tab()
  expect(area).toHaveValue('voiced one')
  expect(caption('written how you write')).toBe(1)
  expect(m.calls.some((c) => c.method === 'PUT')).toBe(false)
})

test('double tap sends one post request', async () => {
  let release!: () => void
  const outcomes: Outcome[] = [
    { channelId: 'c1', service: 'twitter', status: 'queued', when: null, note: '' },
    { channelId: 'c2', service: 'bluesky', status: 'draft', when: null, note: '' },
  ]
  const m = mockApi({
    post: () => new Promise((res) => { release = () => res(new Response(JSON.stringify({ outcomes }), { status: 200 })) }),
  })
  visit()
  const go = await screen.findByRole('button', { name: 'Add to my queue' })
  await userEvent.click(go)
  await userEvent.click(go)
  expect(go).toHaveAttribute('aria-busy', 'true')
  expect(posts(m)).toHaveLength(1)
  const sent = JSON.parse(posts(m)[0].body!)
  expect(sent.selections).toEqual([
    { channelId: 'c1', text: 'template one', timing: 'window', assetUrl: '' },
    { channelId: 'c2', text: 'template two', timing: 'window', assetUrl: '' },
  ])
  release()
  await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent('/s/tok1/done'))
})

test('mixed outcomes render one row per channel, no banner', async () => {
  const when = new Date(2026, 10, 3, 9, 0).toISOString()
  const outcomes: Outcome[] = [
    { channelId: 'c1', service: 'twitter', status: 'queued', when, note: '' },
    { channelId: 'c2', service: 'bluesky', status: 'draft', when: null, note: 'queue full, saved as draft' },
    { channelId: 'c3', service: 'twitter', status: 'failed', when: null, note: 'X rejected: over 280 characters, edit' },
  ]
  mockApi({
    connect: approveView({ channels: [X, BSKY, ch('c3', 'Ada alt', 'twitter')], drafts: [dr('d1', 'c1', 'a'), dr('d2', 'c2', 'b'), dr('d3', 'c3', 'c')] }),
    post: () => Promise.resolve(new Response(JSON.stringify({ outcomes }), { status: 200 })),
  })
  visit()
  await userEvent.click(await screen.findByRole('button', { name: 'Add to my queue' }))
  const list = await screen.findByRole('list', { name: 'Results' })
  const rows = within(list).getAllByRole('listitem')
  expect(rows).toHaveLength(3)
  expect(rows[0]).toHaveTextContent('queued Tue 9:00')
  expect(rows[1]).toHaveTextContent('queue full, saved as draft')
  expect(rows[2]).toHaveTextContent(/^.*X rejected: over 280 characters, edit$/)
  expect(screen.getAllByText(/rejected/)).toHaveLength(1)
  expect(rows[2]).not.toHaveTextContent('rejected: X rejected')
  expect(document.querySelector('.alert')).toBeNull()
  expect(screen.getByRole('button', { name: 'Add to my queue' })).not.toHaveAttribute('aria-busy')
  expect(screen.getByTestId('where')).toHaveTextContent('/s/tok1/approve')
})

test('no eligible channels shows copy path, no post button', async () => {
  const writeText = vi.fn(() => Promise.resolve())
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  mockApi({ connect: approveView({ channels: [ch('c1', 'Ada on X', 'twitter', { eligible: false, checked: false, reason: 'not connected' })] }) })
  visit()
  expect(await screen.findByText('template one')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Add to my queue' })).toBeNull()
  expect(screen.getByRole('link', { name: 'Other ways to share' })).toHaveAttribute('href', '/s/tok1/fallback')
  await userEvent.click(screen.getByRole('button', { name: 'Copy text' }))
  expect(writeText).toHaveBeenCalledWith('template one')
  expect(screen.getByRole('checkbox', { name: /^Ada on X.*not connected$/ })).toBeDisabled()
})

test('closed launch hides window timing', async () => {
  mockApi({ connect: approveView({ closed: true, timing: ['queue', 'draft'] }) })
  visit()
  expect(await screen.findByRole('radio', { name: 'Next free slot' })).toBeChecked()
  expect(screen.queryByRole('radio', { name: 'Land in the window' })).toBeNull()
  expect(screen.getByRole('radio', { name: 'Save as draft' })).toBeInTheDocument()
})

test('reduced motion swaps without transition class', async () => {
  for (const reduce of [true, false]) {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    matchMedia(reduce)
    const m = mockApi({ connect: approveView({ channels: [X], drafts: [dr('d1', 'c1', 'template one')] }) })
    const { unmount } = visit()
    await box()
    m.polled = { voicedReady: true, drafts: [dr('d1', 'c1', 'voiced one', { fromModel: true })] }
    await advance(1500)
    expect(screen.getByRole('textbox', { name: 'Your post' })).toHaveValue('voiced one')
    expect(document.querySelector('.draft-reveal') !== null).toBe(!reduce)
    unmount()
    vi.useRealTimers()
  }
})

test('needs reconnect shows Reconnect Buffer instead of Add to my queue', async () => {
  mockApi({ connect: approveView({ needsReconnect: true }) })
  visit()
  expect(await screen.findByRole('link', { name: 'Reconnect Buffer' })).toHaveAttribute(
    'href', '/auth/buffer/start?return_to=/s/tok1/approve')
  expect(screen.queryByRole('button', { name: 'Add to my queue' })).toBeNull()
})

test('connect errors: unverified goes back, claimed invite is a dead end', async () => {
  mockApi({ connect: { status: 403, error: 'verify_first' } })
  const { unmount } = visit()
  await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent(/^\/s\/tok1$/))
  unmount()
  mockApi({ connect: { status: 409, error: 'already_claimed', message: 'this invite was already connected by its owner' } })
  visit()
  expect(await screen.findByText('this invite was already connected by its owner')).toBeInTheDocument()
})

test('Buffer not answering keeps edits and window_closed switches timing', async () => {
  let n = 0
  const m = mockApi({
    post: () => Promise.resolve(n++ === 0
      ? new Response(JSON.stringify({ error: 'buffer_unavailable' }), { status: 502 })
      : new Response(JSON.stringify({ error: 'window_closed', message: 'The window has closed' }), { status: 409 })),
  })
  visit()
  const area = await box()
  await userEvent.type(area, ' more')
  await userEvent.click(screen.getByRole('button', { name: 'Add to my queue' }))
  expect(await screen.findByText('Buffer is not answering, try again')).toBeInTheDocument()
  expect(area).toHaveValue('template one more')
  await userEvent.click(screen.getByRole('button', { name: 'Add to my queue' }))
  expect(await screen.findByText('The window has closed')).toBeInTheDocument()
  expect(screen.getByRole('radio', { name: 'Next free slot' })).toBeChecked()
  expect(posts(m)).toHaveLength(2)
})

test('disconnect needs a confirm tap', async () => {
  const m = mockApi()
  visit()
  await screen.findByRole('button', { name: 'Add to my queue' })
  expect(document.body).toHaveTextContent('adds drafts to your queue only · nothing posts without your tap · disconnect any time')
  await userEvent.click(screen.getByRole('button', { name: 'disconnect' }))
  expect(screen.getByText('Disconnect Buffer?')).toBeInTheDocument()
  expect(m.calls.some((c) => c.url.endsWith('/disconnect'))).toBe(false)
  await userEvent.click(screen.getByRole('button', { name: 'Yes' }))
  await waitFor(() => expect(m.calls.some((c) => c.method === 'POST' && c.url === '/api/crew/m1/disconnect')).toBe(true))
  await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent(/^\/s\/tok1$/))
})

test('polling stops after 20 s and template drafts keep no caption', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const m = mockApi({ connect: approveView({ channels: [X], drafts: [dr('d1', 'c1', 'template one')] }) })
  m.polled = { voicedReady: false, drafts: [dr('d1', 'c1', 'template one')] }
  visit()
  await box()
  await advance(21000)
  const polls = () => m.calls.filter((c) => c.url.endsWith('/drafts')).length
  const n = polls()
  expect(n).toBeGreaterThan(5)
  expect(caption('a version in your voice is on its way')).toBe(0)
  await advance(5000)
  expect(polls()).toBe(n)
})

test('disconnect failure stays on the page with an alert', async () => {
  mockApi()
  const base = (globalThis.fetch as ReturnType<typeof vi.fn>).getMockImplementation()!
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) =>
    url.endsWith('/disconnect') ? Promise.resolve(new Response('{}', { status: 502 })) : base(url, init)))
  visit()
  await userEvent.click(await screen.findByRole('button', { name: 'disconnect' }))
  await userEvent.click(screen.getByRole('button', { name: 'Yes' }))
  expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't disconnect, try again")
  expect(screen.getByTestId('where')).toHaveTextContent('/s/tok1/approve')
})

test('non-502 post errors show the server message', async () => {
  mockApi({ post: () => Promise.resolve(new Response(JSON.stringify({ error: 'expired', message: 'This link has expired' }), { status: 410 })) })
  visit()
  await userEvent.click(await screen.findByRole('button', { name: 'Add to my queue' }))
  expect(await screen.findByText('This link has expired')).toBeInTheDocument()
  expect(screen.queryByText('Buffer is not answering, try again')).toBeNull()
})

// jsdom has no layout, so offsetTop can't be measured: assert the card follows the
// primary button in document order, so opening it can't push the button down.
test('card follows the primary button in DOM order (offsetTop not measurable in jsdom)', async () => {
  mockApi({ connect: approveView({ voice: { sentenceLength: 'short', case: 'lowercase', hashtags: 'none', extra: '' } }) })
  visit()
  const go = await screen.findByRole('button', { name: 'Add to my queue' })
  const tune = await screen.findByRole('button', { name: 'tune' })
  expect(tune).toHaveAttribute('aria-expanded', 'false')
  await userEvent.click(tune)
  expect(tune).toHaveAttribute('aria-expanded', 'true')
  const card = screen.getByRole('region', { name: 'Voice' })
  expect(go.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(screen.getByRole('radio', { name: 'short' })).toBeChecked()
  await userEvent.click(screen.getByRole('radio', { name: 'medium' }))
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('region', { name: 'Voice' })).toBeNull()
  expect(tune).toHaveFocus()
})

test('saving the voice updates the line, restarts the poll and keeps edits', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const m = mockApi({ connect: approveView({ voicedReady: true, channels: [X], drafts: [dr('d1', 'c1', 'voiced one', { fromModel: true })] }) })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  visit()
  const area = await box()
  await user.type(area, '!')
  await user.click(screen.getByRole('button', { name: 'tune' }))
  await user.click(screen.getByRole('radio', { name: 'long' }))
  await user.click(screen.getByRole('button', { name: 'Rewrite in this voice' }))
  expect(await screen.findByText(/Short, lowercase\./)).toBeInTheDocument()
  const put = m.calls.find((c) => c.url.endsWith('/voice'))!
  expect(put.method).toBe('PUT')
  expect(JSON.parse(put.body!).sentenceLength).toBe('long')
  expect(screen.getByText('your edits are kept; new drafts will use this voice')).toBeInTheDocument()

  m.polled = { voicedReady: true, drafts: [dr('d1', 'c1', 'rewritten', { fromModel: true })] }
  await advance(1600)
  expect(area).toHaveValue('voiced one!') // edited: never replaced
})

test('unedited draft is replaced by the rewrite after a voice save', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const m = mockApi({ connect: approveView({ voicedReady: true, channels: [X], drafts: [dr('d1', 'c1', 'voiced one', { fromModel: true })] }) })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  visit()
  const area = await box()
  await user.click(screen.getByRole('button', { name: 'tune' }))
  await user.click(screen.getByRole('button', { name: 'Rewrite in this voice' }))
  await screen.findByText(/Short, lowercase\./)
  expect(caption('a version in your voice is on its way')).toBe(1)
  expect(screen.queryByText('your edits are kept; new drafts will use this voice')).toBeNull()
  m.polled = { voicedReady: true, drafts: [dr('d1', 'c1', 'rewritten', { fromModel: true })] }
  await advance(1600)
  expect(area).toHaveValue('rewritten')
})

test('refresh channels shows a channel added in Buffer after connecting, and keeps the ticks', async () => {
  const LI = ch('c3', 'Ada on LinkedIn', 'linkedin')
  const m = mockApi({
    refresh: () => Promise.resolve(new Response(JSON.stringify(approveView({
      channels: [X, BSKY, LI],
      drafts: [dr('d1', 'c1', 'template one'), dr('d2', 'c2', 'template two', { network: 'bluesky' }), dr('d3', 'c3', 'template three', { network: 'linkedin' })],
    })), { status: 200 })),
  })
  const user = userEvent.setup()
  visit()
  await box()
  expect(screen.queryByRole('checkbox', { name: /Ada on LinkedIn/ })).toBeNull()
  await user.click(screen.getByRole('checkbox', { name: /^ada\.bsky\.social/ })) // untick one
  await user.click(screen.getByRole('button', { name: 'Refresh channels' }))
  expect(await screen.findByText('Checked Buffer: 3 channels.')).toBeInTheDocument()
  expect(m.calls.some((c) => c.method === 'POST' && c.url === '/api/crew/m1/channels/refresh')).toBe(true)
  expect(screen.getByRole('checkbox', { name: /Ada on LinkedIn/ })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: /^ada\.bsky\.social/ })).not.toBeChecked()
  expect(screen.getByRole('tab', { name: /^Ada on LinkedIn/ })).toBeInTheDocument()
})

test('refresh channels says so when Buffer cannot be reached, and can be tried again', async () => {
  mockApi({ refresh: () => Promise.resolve(new Response(JSON.stringify({ error: 'buffer_unavailable', message: 'x' }), { status: 502 })) })
  const user = userEvent.setup()
  visit()
  await box()
  await user.click(screen.getByRole('button', { name: 'Refresh channels' }))
  expect(await screen.findByText('Could not reach Buffer, try again.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Refresh channels' })).toBeEnabled()
})
