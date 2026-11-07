import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import App from '../App'
import type { Check, Detail, LaunchSummary } from '../api'

const checks = (red: number): Check[] =>
  Array.from({ length: 7 }, (_, i) => ({
    id: i + 1,
    name: `Check ${i + 1}`,
    ok: i >= red,
    fix: i < red ? `Fix ${i + 1}` : '',
  }))

const detail = (over: Partial<Detail> = {}): Detail => ({
  id: 'l1',
  status: 'draft',
  source: 'github',
  title: 'rocket v1.2.0',
  summary: 's',
  releaseTag: 'v1.2.0',
  releaseUrl: 'https://github.com/acme/rocket/releases/tag/v1.2.0',
  launchAt: null,
  windowMinutes: 90,
  crewCap: 200,
  openCount: 0,
  openUrl: '',
  showUrl: '',
  pngUrl: '',
  roles: [],
  bufferConnected: true,
  headlines: [{ prNumber: 11, title: 'Faster boosters', authorLogin: 'alice', hidden: false }],
  chores: [],
  captainDrafts: [],
  crew: [],
  checks: checks(0),
  deadLetters: [],
  counts: { in: 0, total: 0 },
  captainName: 'Shubham Tomar',
  qrLogo: 'plane',
  qrLogoUrl: '',
  ...over,
})

type Call = { method: string; url: string; body?: unknown }

let refreshReply = () => Promise.resolve(new Response(JSON.stringify({ channels: 3 }), { status: 200 }))

function mockApi(routes: { detail?: Detail; list?: LaunchSummary[] } = {}) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const ok = (b: unknown, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s }))
      if (url === '/api/captain/launches' && method === 'GET') return ok(routes.list ?? [])
      if (url.endsWith('/channels/refresh')) return refreshReply()
      if (url.includes('/retry/') || url.endsWith('/retry-comment')) return ok({ status: 'retrying' }, 202)
      return ok(routes.detail ?? detail())
    }),
  )
  return calls
}

function visit(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
  refreshReply = () => Promise.resolve(new Response(JSON.stringify({ channels: 3 }), { status: 200 }))
})

test('launch disabled label counts red checks', async () => {
  mockApi({ detail: detail({ checks: checks(2) }) })
  visit('/captain/launches/l1')
  const btn = await screen.findByRole('button', { name: 'Launch · 2 checks left' })
  expect(btn).toBeDisabled()
})

test('launch enabled when all checks are green', async () => {
  mockApi()
  visit('/captain/launches/l1')
  expect(await screen.findByRole('button', { name: 'Launch' })).toBeEnabled()
})

test('dead letter row first with retry', async () => {
  const calls = mockApi({
    detail: detail({ deadLetters: [{ jobId: 42, kind: 'launch', lastError: 'preflight not green', attempts: 5 }] }),
  })
  visit('/captain/launches/l1')
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('preflight not green')
  const h1 = screen.getByRole('heading', { level: 1 })
  expect(alert.compareDocumentPosition(h1) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  const main = screen.getByRole('main')
  expect(within(main).getAllByRole('button')[0]).toBe(within(alert).getByRole('button', { name: 'Retry' }))
  await userEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
  expect(calls.some((c) => c.method === 'POST' && c.url === '/api/captain/launches/l1/retry/42')).toBe(true)
})

test('red preflight row shows fix text', async () => {
  mockApi({ detail: detail({ checks: checks(1) }) })
  visit('/captain/launches/l1')
  const section = await screen.findByRole('region', { name: 'Preflight' })
  const list = within(section).getByRole('list')
  expect(list.tagName).toBe('OL')
  expect(within(list).getAllByRole('listitem')).toHaveLength(7)
  expect(within(section).getByText('Fix 1')).toBeInTheDocument()
  expect(within(section).queryByText('Fix 2')).toBeNull()
})

test('empty launches state', async () => {
  mockApi({ list: [] })
  visit('/captain')
  expect(await screen.findByText(/No launches yet/)).toBeInTheDocument()
  expect(screen.getAllByRole('link', { name: 'New launch' }).some((a) => a.getAttribute('href') === '/captain/new')).toBe(true)
  expect(screen.queryByRole('link', { name: 'Start one by hand' })).toBeNull()
})

test('new launch roles capped at four and 24 chars', async () => {
  mockApi()
  visit('/captain/new')
  const add = await screen.findByRole('button', { name: 'Add a role' })
  for (let i = 0; i < 6; i++) if (!(add as HTMLButtonElement).disabled) await userEvent.click(add)
  const names = screen.getAllByLabelText(/^Role \d name$/)
  expect(names).toHaveLength(4)
  names.forEach((n) => expect(n).toHaveAttribute('maxlength', '24'))
  expect(add).toBeDisabled()
})

test('hero url must be https', async () => {
  const calls = mockApi()
  visit('/captain/new')
  await userEvent.type(await screen.findByLabelText('Title'), 'Go night')
  await userEvent.type(screen.getByLabelText('Hero image URL (optional)'), 'http://x.test/a.png')
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(await screen.findByText('Use an https:// image URL.')).toBeInTheDocument()
  expect(calls.some((c) => c.method === 'POST')).toBe(false)
})

test('crew state words drop underscores', async () => {
  const row = { id: 'm1', name: 'carol', role: 'contributor', state: 'opted_out', commentFailed: false, commentRetriesLeft: 0, inviteUrl: '' }
  mockApi({ detail: detail({ crew: [row as unknown as Detail['crew'][number]], counts: { in: 0, total: 1 } }) })
  visit('/captain/launches/l1')
  expect(await screen.findByText('opted out')).toBeInTheDocument()
})

const post = (over: Partial<Detail['captainDrafts'][number]>): Detail['captainDrafts'][number] => ({
  id: 'p1', channelId: 'c1', channelName: 'Acme X', service: 'twitter', text: 'v1.2', status: 'queued', bufferPostId: 'b1', note: '', ...over,
})

test('captain posts show draft and failed notes', async () => {
  mockApi({
    detail: detail({
      status: 'launched',
      captainDrafts: [
        post({ id: 'a', channelName: 'Acme X', status: 'draft', note: 'your queue is full, saved as a draft instead' }),
        post({ id: 'b', channelName: 'Acme LinkedIn', service: 'linkedin', status: 'failed', bufferPostId: '', note: 'Buffer refused it: Text too long' }),
      ],
    }),
  })
  visit('/captain/launches/l1')
  const posts = await screen.findByRole('region', { name: 'Your posts' })
  expect(within(posts).getByText('your queue is full, saved as a draft instead')).toBeInTheDocument()
  const failed = within(posts).getByText('Buffer refused it: Text too long')
  expect(failed.closest('li')).toHaveTextContent('failed')
})

test('a posted captain post says Posted and has no edit or remove buttons', async () => {
  mockApi({
    detail: detail({
      status: 'launched',
      captainDrafts: [
        post({ id: 'a', channelName: 'Acme X', status: 'posted' }),
        post({ id: 'b', channelName: 'Acme LinkedIn', service: 'linkedin', status: 'queued', bufferPostId: 'b2' }),
      ],
    }),
  })
  visit('/captain/launches/l1')
  const posts = await screen.findByRole('region', { name: 'Your posts' })
  expect(within(posts).getByText('Posted', { exact: false }).closest('li')).toHaveTextContent('Acme X')
  expect(within(posts).getAllByText('Posted', { exact: false })).toHaveLength(1)
  expect(within(posts).queryByRole('button', { name: /Edit draft for Acme X/ })).toBeNull()
  expect(within(posts).queryByRole('button', { name: /Remove/ })).toBeNull()
})

test('manual draft promises links after launch', async () => {
  mockApi({ detail: detail({ source: 'manual', headlines: [] }) })
  visit('/captain/launches/l1')
  expect(await screen.findByText('Link, slide and PNG appear after Launch.')).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: 'Show QR at the event' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'Copy link' })).toBeNull()
})

test('launched manual launch shows link, slide and PNG', async () => {
  mockApi({
    detail: detail({ source: 'manual', status: 'launched', openUrl: 'https://lc.test/l/t', showUrl: 'https://lc.test/l/t/show', pngUrl: 'https://lc.test/api/l/t/show.png' }),
  })
  visit('/captain/launches/l1')
  expect(await screen.findByRole('link', { name: 'Show QR at the event' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Copy link' })).toBeInTheDocument()
  expect(screen.queryByText('Link, slide and PNG appear after Launch.')).toBeNull()
})

test('a captain post can be edited in place while the launch is a draft', async () => {
  const d = detail({
    captainDrafts: [
      { id: 'cd1', channelId: 'c1', channelName: 'launchcrew', service: 'twitter', text: 'old text', status: 'draft', bufferPostId: '', note: '' },
    ],
  })
  const calls = mockApi({ detail: d })
  const user = userEvent.setup()
  visit('/captain/launches/l1')
  await user.click(await screen.findByRole('button', { name: /edit draft/i }))
  const box = screen.getByRole('textbox', { name: /edit post for launchcrew/i })
  await user.clear(box)
  await user.type(box, 'a better post')
  await user.click(screen.getByRole('button', { name: /save post/i }))
  const patch = calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/drafts/cd1'))
  expect(patch?.body).toEqual({ text: 'a better post' })
})

test('a post that already went to Buffer has no edit button', async () => {
  mockApi({
    detail: detail({
      captainDrafts: [
        { id: 'cd1', channelId: 'c1', channelName: 'launchcrew', service: 'twitter', text: 'sent', status: 'draft', bufferPostId: 'p1', note: '' },
      ],
    }),
  })
  visit('/captain/launches/l1')
  await screen.findByText('sent')
  expect(screen.queryByRole('button', { name: /edit draft/i })).toBeNull()
})

test('the appearance switch lives in the top bar', async () => {
  mockApi()
  visit('/captain/launches/l1')
  const nav = (await screen.findAllByRole('banner'))[0]
  expect(within(nav).getByRole('radio', { name: 'Dark' })).toBeInTheDocument()
})

const cd = (over: Partial<Detail['captainDrafts'][number]> = {}): Detail['captainDrafts'][number] => ({
  id: 'cd1', channelId: 'c1', channelName: 'launchcrew', service: 'twitter', text: 'hello', status: 'draft', bufferPostId: '', note: '', ...over,
})
const member = (over: Partial<Detail['crew'][number]> = {}): Detail['crew'][number] => ({
  id: 'm1', name: 'Maya Kapoor', login: 'mayakp', role: 'contributor', state: 'approved', commentFailed: false, commentRetriesLeft: 0, ...over,
})

test('hero shows the title once, the crew facts and the countdown', async () => {
  const at = new Date(Date.now() + 2 * 3600_000 + 30 * 60_000).toISOString()
  mockApi({
    detail: detail({
      launchAt: at,
      crew: [member(), member({ id: 'm2', name: 'Devin Osei', state: 'drafting' }), member({ id: 'm3', name: 'Priya Nair', state: 'invited' })],
      counts: { in: 1, total: 3 },
    }),
  })
  visit('/captain/launches/l1')
  const hero = await screen.findByRole('region', { name: 'Launch summary' })
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
  expect(within(hero).getByRole('heading', { level: 1 })).toHaveTextContent('rocket v1.2.0')
  expect(within(hero).getByText('Crew approving')).toBeInTheDocument()
  expect(within(hero).getByText('1 PR')).toBeInTheDocument()
  expect(within(hero).getByText('4 crew')).toBeInTheDocument() // three people plus the captain
  expect(within(hero).getByText('2 approved')).toBeInTheDocument() // one approved plus the captain
  expect(within(hero).getByText('1 drafting')).toBeInTheDocument()
  expect(within(hero).getByText('1 invited')).toBeInTheDocument()
  expect(within(hero).getByText('Window opens in')).toBeInTheDocument()
  expect(within(hero).getByText(/^2h 30m$|^2h 29m$/)).toBeInTheDocument()
})

test('hero says Draft with no crew and asks for a time when none is set', async () => {
  mockApi({ detail: detail() })
  visit('/captain/launches/l1')
  const hero = await screen.findByRole('region', { name: 'Launch summary' })
  expect(within(hero).getByText('Draft')).toBeInTheDocument()
  expect(within(hero).getByText('Pick a launch time')).toBeInTheDocument()
})

test('a launched launch says Posting and never shows an opening countdown', async () => {
  mockApi({ detail: detail({ status: 'launched', launchAt: new Date(Date.now() - 600_000).toISOString() }) })
  visit('/captain/launches/l1')
  const hero = await screen.findByRole('region', { name: 'Launch summary' })
  expect(within(hero).getByText('Posting')).toBeInTheDocument()
  expect(within(hero).queryByText('Window opens in')).toBeNull()
})

test('the captain is the first row of the crew and is counted', async () => {
  mockApi({ detail: detail({ crew: [member()], counts: { in: 1, total: 1 } }) })
  visit('/captain/launches/l1')
  const crew = await screen.findByRole('region', { name: 'The crew · 2' })
  const rows = within(crew).getAllByRole('listitem')
  expect(rows[0]).toHaveTextContent('Shubham Tomar')
  expect(rows[0]).toHaveTextContent('Captain')
  expect(rows[0]).toHaveTextContent('you')
  expect(rows[1]).toHaveTextContent('Maya Kapoor')
  expect(within(crew).getByText('2 of 2 in')).toBeInTheDocument()
  for (const r of rows) expect(r).toHaveClass('cap-crew-grid')
})

test('with nobody else, the crew is the captain alone and says so', async () => {
  mockApi({ detail: detail() })
  visit('/captain/launches/l1')
  const crew = await screen.findByRole('region', { name: 'The crew · 1' })
  expect(within(crew).getByText('1 of 1 in')).toBeInTheDocument()
  expect(within(crew).getByText('No contributors found in this release.')).toBeInTheDocument()
})

test('add by link is a labelled read-only field with a copy button beside it', async () => {
  mockApi({ detail: detail({ source: 'manual', openUrl: 'https://lc.test/l/abc' }) })
  visit('/captain/launches/l1')
  const field = await screen.findByLabelText('Add by link')
  expect(field).toHaveAttribute('readonly')
  expect(field).toHaveValue('https://lc.test/l/abc')
  expect(screen.getByRole('button', { name: 'Copy link' })).toHaveClass('btn-secondary')
})

test('a captain post can be removed from its channel and added back', async () => {
  const calls = mockApi({ detail: detail({ captainDrafts: [cd(), cd({ id: 'cd2', channelName: 'Shubham Tomar', service: 'linkedin' })] }) })
  const user = userEvent.setup()
  visit('/captain/launches/l1')
  expect(await screen.findByText('Posts go to each channel you keep. Remove one to skip that channel.')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /remove post for launchcrew/i }))
  const patch = calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/drafts/cd1'))
  expect(patch?.body).toEqual({ skipped: true })
})

test('a removed post is collapsed, cannot be edited, and can be added back', async () => {
  const calls = mockApi({ detail: detail({ captainDrafts: [cd({ status: 'skipped' })] }) })
  const user = userEvent.setup()
  visit('/captain/launches/l1')
  expect(await screen.findByText(/Not posting to launchcrew · X/)).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /edit draft/i })).toBeNull()
  expect(screen.queryByText('hello')).toBeNull()
  await user.click(screen.getByRole('button', { name: /add back/i }))
  const patch = calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/drafts/cd1'))
  expect(patch?.body).toEqual({ skipped: false })
})

test('removed posts are not counted as drafts of yours', async () => {
  mockApi({ detail: detail({ captainDrafts: [cd(), cd({ id: 'cd2', status: 'skipped' })] }) })
  visit('/captain/launches/l1')
  expect(await screen.findByText('1 of yours')).toBeInTheDocument()
})

test('manual launches get a Share at the event panel with real buttons', async () => {
  mockApi({ detail: detail({ source: 'manual', showUrl: '/l/abc/show', pngUrl: '/l/abc/card.png' }) })
  visit('/captain/launches/l1')
  const panel = await screen.findByRole('region', { name: 'Share at the event' })
  const show = within(panel).getByRole('link', { name: 'Show QR at the event' })
  const png = within(panel).getByRole('link', { name: 'Download PNG' })
  expect(show).toHaveClass('btn', 'btn-secondary')
  // The slide opens with a full page load, so it is told which launch Close returns to.
  expect(show).toHaveAttribute('href', '/l/abc/show?back=%2Fcaptain%2Flaunches%2Fl1')
  expect(png).toHaveClass('btn', 'btn-secondary')
  expect(png).toHaveAttribute('href', '/l/abc/card.png')
  expect(png).toHaveAttribute('download')
})

test('before launch the share hint stays and there is no share panel', async () => {
  mockApi({ detail: detail({ source: 'manual', showUrl: '' }) })
  visit('/captain/launches/l1')
  expect(await screen.findByText('Link, slide and PNG appear after Launch.')).toBeInTheDocument()
  expect(screen.queryByRole('region', { name: 'Share at the event' })).toBeNull()
})

test('refresh channels calls the API, reloads and shows the count', async () => {
  const calls = mockApi()
  visit('/captain/launches/l1')
  await userEvent.click(await screen.findByRole('button', { name: 'Refresh channels' }))
  expect(await screen.findByText('Checked Buffer: 3 channels.')).toBeInTheDocument()
  const i = calls.findIndex((c) => c.method === 'POST' && c.url === '/api/captain/launches/l1/channels/refresh')
  expect(i).toBeGreaterThan(-1)
  expect(calls.slice(i + 1).some((c) => c.method === 'GET' && c.url === '/api/captain/launches/l1')).toBe(true)
})

test('refresh channels shows an error when Buffer cannot be reached', async () => {
  mockApi()
  refreshReply = () => Promise.resolve(new Response(JSON.stringify({ error: 'internal', message: 'x' }), { status: 500 }))
  visit('/captain/launches/l1')
  await userEvent.click(await screen.findByRole('button', { name: 'Refresh channels' }))
  expect(await screen.findByText('Could not reach Buffer, try again.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Refresh channels' })).toBeEnabled()
})

test('no refresh channels button once launched or without Buffer', async () => {
  mockApi({ detail: detail({ status: 'launched' }) })
  const { unmount } = visit('/captain/launches/l1')
  await screen.findByRole('heading', { level: 1 })
  expect(screen.queryByRole('button', { name: 'Refresh channels' })).toBeNull()
  unmount()
  mockApi({ detail: detail({ bufferConnected: false }) })
  visit('/captain/launches/l1')
  await screen.findByRole('heading', { level: 1 })
  expect(screen.queryByRole('button', { name: 'Refresh channels' })).toBeNull()
})

const failed = (over: Partial<Detail['crew'][number]> = {}) =>
  member({ state: 'invited', commentFailed: true, commentRetriesLeft: 3, inviteUrl: 'https://lc.test/s/tok', ...over })

test('a failed PR comment can be retried, and the page reloads', async () => {
  const calls = mockApi({ detail: detail({ status: 'launched', crew: [failed()], counts: { in: 0, total: 1 } }) })
  visit('/captain/launches/l1')
  expect(await screen.findByText('comment failed')).toBeInTheDocument()
  expect(screen.getByText('3 retries left')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Copy link' })).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Retry comment for Maya Kapoor' }))
  const i = calls.findIndex((c) => c.method === 'POST' && c.url === '/api/captain/launches/l1/crew/m1/retry-comment')
  expect(i).toBeGreaterThan(-1)
  expect(calls.slice(i + 1).some((c) => c.method === 'GET' && c.url === '/api/captain/launches/l1')).toBe(true)
})

test('no retry button once the retries are used up, the link stays', async () => {
  mockApi({ detail: detail({ status: 'launched', crew: [failed({ commentRetriesLeft: 0 })], counts: { in: 0, total: 1 } }) })
  visit('/captain/launches/l1')
  expect(await screen.findByText('Retried the most times allowed.')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /Retry comment/ })).toBeNull()
  expect(screen.getByRole('button', { name: 'Copy link' })).toBeInTheDocument()
})

test('no retry button for a comment that did not fail', async () => {
  mockApi({ detail: detail({ status: 'launched', crew: [member({ state: 'invited' })], counts: { in: 0, total: 1 } }) })
  visit('/captain/launches/l1')
  await screen.findByText('Maya Kapoor')
  expect(screen.queryByRole('button', { name: /Retry comment/ })).toBeNull()
})

test('offline shows a plain message and Retry loads the launch', async () => {
  const fetchMock = vi.fn()
    .mockRejectedValueOnce(new TypeError('Failed to fetch'))
    .mockResolvedValue(new Response(JSON.stringify(detail()), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  visit('/captain/launches/l1')
  expect(await screen.findByRole('heading', { level: 1, name: 'Could not load this launch' })).toBeInTheDocument()
  expect(screen.getByText('Could not reach Launch Crew. Check your connection and try again.')).toBeInTheDocument()
  expect(screen.queryByText(/Failed to fetch/)).toBeNull()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('heading', { level: 1, name: 'rocket v1.2.0' })).toBeInTheDocument()
})

test('a 404 launch has no Retry, since trying again will not help', async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify({ error: 'not_found' }), { status: 404 }))))
  visit('/captain/launches/l1')
  expect(await screen.findByRole('heading', { level: 1, name: 'Launch not found' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
})

test('when the clipboard is blocked the link field is selected and the button says so', async () => {
  mockApi({ detail: detail({ source: 'manual', openUrl: 'https://lc.test/l/abc' }) })
  vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
  visit('/captain/launches/l1')
  const button = await screen.findByRole('button', { name: 'Copy link' })
  await userEvent.click(button)
  expect(await screen.findByRole('button', { name: 'Select and copy' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Copied' })).toBeNull()
  const field = screen.getByLabelText('Add by link') as HTMLInputElement
  expect(field).toHaveFocus()
  expect(field.selectionEnd! - field.selectionStart!).toBe(field.value.length)
})

// 140 s sits between the rounding boundaries of until() (2.33 min, then 1.33, then 0.33),
// so a few seconds of real time on a slow CI runner cannot flip a tick early.
test('the hero countdown ticks and asks for a later time once the moment passes', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  try {
    mockApi({ detail: detail({ launchAt: new Date(Date.now() + 140_000).toISOString() }) })
    visit('/captain/launches/l1')
    const side = (await screen.findByText('Window opens in')).parentElement!
    expect(within(side).getByText('2m')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(within(side).getByText('1m')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(within(side).getByText('Pick a later time')).toBeInTheDocument()
  } finally {
    vi.useRealTimers()
  }
})

test('a sent captain post links to it on the network and in Buffer', async () => {
  mockApi({
    detail: detail({
      status: 'closed',
      captainDrafts: [
        cd({ status: 'posted', bufferPostId: 'p1', externalLink: 'https://x.com/launchcrew/status/1' }),
        cd({ id: 'cd2', channelId: 'c2', channelName: 'Acme LinkedIn', service: 'linkedin', status: 'queued', bufferPostId: 'p2' }),
      ],
    }),
  })
  visit('/captain/launches/l1')
  const posts = await screen.findByRole('region', { name: 'Your posts' })
  expect(within(posts).getByRole('link', { name: 'View on X' })).toHaveAttribute('href', 'https://x.com/launchcrew/status/1')
  const buffer = within(posts).getAllByRole('link', { name: 'Open in Buffer' })
  expect(buffer).toHaveLength(2)
  expect(buffer[0]).toHaveAttribute('href', 'https://publish.buffer.com/channels/c1/sent')
  expect(buffer[1]).toHaveAttribute('href', 'https://publish.buffer.com/channels/c2/queue')
  expect(within(posts).queryByRole('link', { name: 'View on LinkedIn' })).toBeNull()
})

test("a crew member's posts link out once sent and say where they are until then", async () => {
  mockApi({
    detail: detail({
      status: 'closed',
      crew: [member({ state: 'posted', posts: [{ network: 'twitter', status: 'posted', externalLink: 'https://x.com/mayakp/status/7' }, { network: 'bluesky', status: 'queued', externalLink: '' }] })],
      counts: { in: 1, total: 1 },
    }),
  })
  visit('/captain/launches/l1')
  const list = await screen.findByRole('list', { name: 'Posts by Maya Kapoor' })
  expect(within(list).getByRole('link', { name: 'View on X' })).toHaveAttribute('href', 'https://x.com/mayakp/status/7')
  expect(within(list).getByText('Bluesky · queued')).toBeInTheDocument()
})

test('a sent crew post without its link yet does not repeat "posted" under the badge', async () => {
  mockApi({
    detail: detail({
      status: 'closed',
      crew: [member({ state: 'posted', posts: [{ network: 'twitter', status: 'posted', externalLink: '' }] })],
      counts: { in: 1, total: 1 },
    }),
  })
  visit('/captain/launches/l1')
  const list = await screen.findByRole('list', { name: 'Posts by Maya Kapoor' })
  expect(within(list).getByText('X · link not ready yet')).toBeInTheDocument()
  expect(within(list).queryByText(/· posted/)).toBeNull()
})

test('post actions and the PR hide toggle are icon buttons with full names', async () => {
  mockApi({ detail: detail({ captainDrafts: [cd()] }) })
  visit('/captain/launches/l1')
  const edit = await screen.findByRole('button', { name: 'Edit draft for launchcrew' })
  expect(edit).toHaveClass('btn-icon')
  expect(edit).toHaveAttribute('title', 'Edit draft for launchcrew')
  expect(screen.getByRole('button', { name: 'Remove post for launchcrew' })).toHaveClass('btn-icon')
  expect(screen.getByRole('button', { name: 'Hide #11' })).toHaveAttribute('aria-pressed', 'false')
  expect(screen.getByRole('button', { name: 'Refresh channels' })).toHaveClass('btn-icon')
})

test('hidden chores fold away behind a toggle that says Show, then Hide', async () => {
  mockApi({ detail: detail({ chores: [{ prNumber: 3, title: 'Bump deps', authorLogin: 'bot', hidden: true }] }) })
  visit('/captain/launches/l1')
  const toggle = await screen.findByText('1 hidden chore')
  const details = toggle.closest('details')!
  expect(details.open).toBe(false)
  expect(within(details).getByText('Show')).toBeInTheDocument()
  await userEvent.click(toggle)
  expect(details.open).toBe(true)
  expect(within(details).getByText('Hide')).toBeInTheDocument()
  expect(within(details).getByRole('button', { name: 'Keep #3' })).toBeInTheDocument()
})

test('while the launch loads the page shows shimmering placeholders, not a spinner', async () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
  const { container } = visit('/captain/launches/l1')
  expect(screen.getByRole('status')).toHaveTextContent('Loading this launch')
  expect(container.querySelectorAll('.skeleton-line').length).toBeGreaterThan(3)
})

test('the release card and each crew card open from a fold', async () => {
  mockApi({
    detail: detail({
      cardUrl: 'https://cdn.example/cards/release.png',
      crew: [
        { id: 'm1', name: 'ada', login: 'ada', role: 'contributor', state: 'invited', commentFailed: false, commentRetriesLeft: 3, cardUrl: 'https://cdn.example/cards/ada.png', posts: [] },
        { id: 'm2', name: 'bob', login: 'bob', role: 'contributor', state: 'suggested', commentFailed: false, commentRetriesLeft: 3, posts: [] },
      ],
    }),
  })
  visit('/captain/launches/l1')
  const release = await screen.findByText('Release card')
  expect(release.closest('details')!.open).toBe(false)
  await userEvent.click(release)
  expect(screen.getByRole('img', { name: /^Release card for / })).toHaveAttribute('src', 'https://cdn.example/cards/release.png')

  const cards = screen.getAllByText('Card')
  expect(cards).toHaveLength(1) // bob has no card yet
  await userEvent.click(cards[0])
  expect(screen.getByRole('img', { name: 'Contributor card for ada' })).toHaveAttribute('src', 'https://cdn.example/cards/ada.png')
})
