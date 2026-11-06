import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { readFileSync } from 'node:fs'
import { MemoryRouter } from 'react-router'
import App from '../App'
import type { View } from '../api'

const base: View = {
  launch: {
    id: 'l1', title: 'Ship the thing', summary: 's', project: 'acme', version: 'v1.2.0',
    releaseUrl: 'https://github.com/acme/x/releases/v1.2.0', status: 'live',
    launchAt: '2026-11-10T10:00:00Z', windowMinutes: 90, source: 'github', shortUrl: '',
  },
  member: { id: 'm1', name: 'Ada', role: 'contributor', roleHint: '', state: 'invited', verified: true },
  card: { url: '/cards/m1.png', alt: 'Ada shipped Ship the thing' },
  preview: 'I helped ship the thing in acme v1.2.0.',
  verified: true,
  foreign: false,
  needsVerify: false,
  connected: false,
  copyText: 'Neutral draft text',
}

function reply(status: number, body: unknown) {
  return Promise.resolve(new Response(JSON.stringify(body), { status }))
}

function mockApi(view: Partial<View> | { status: number; error: string }) {
  const calls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? 'GET'} ${url}`)
      if (url.endsWith('/share')) return reply(200, { text: 'Neutral draft text' })
      if ('status' in view) return reply(view.status, { error: view.error, message: 'm' })
      return reply(200, { ...base, ...view })
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
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('renders every View state', () => {
  test('normal', async () => {
    const calls = mockApi({})
    visit('/s/tok1')
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Ship the thing')
    expect(screen.getAllByText('Ship the thing')).toHaveLength(1)
    expect(screen.getByRole('link', { name: 'Connect Buffer & share' })).toHaveAttribute(
      'href', '/auth/buffer/start?return_to=/s/tok1/approve')
    expect(screen.getByRole('link', { name: "I don't use Buffer" })).toBeInTheDocument()
    expect(screen.getByText('I helped ship the thing in acme v1.2.0.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings')
    expect(screen.getByText('Built on the Buffer API')).toBeInTheDocument()
    expect(calls[0]).toBe('GET /api/s/tok1')
  })

  test('already connected shows Continue to approve', async () => {
    mockApi({ connected: true })
    visit('/s/tok1')
    const cont = await screen.findByRole('link', { name: 'Continue' })
    expect(cont).toHaveAttribute('href', '/s/tok1/approve')
    expect(cont).toHaveClass('btn-primary')
    expect(screen.queryByRole('link', { name: 'Connect Buffer & share' })).toBeNull()
    expect(screen.getByRole('link', { name: "I don't use Buffer" })).toBeInTheDocument()
  })

  test('not connected keeps Connect Buffer & share', async () => {
    mockApi({ connected: false })
    visit('/s/tok1')
    expect(await screen.findByRole('link', { name: 'Connect Buffer & share' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Continue' })).toBeNull()
  })

  test('unverified GitHub contributor', async () => {
    mockApi({ needsVerify: true, verified: false, card: null })
    visit('/s/tok1')
    const verify = await screen.findByRole('link', { name: "Verify it's you with GitHub" })
    expect(verify).toHaveAttribute('href', '/github/verify/start?crew=m1&t=tok1')
    expect(screen.queryByRole('link', { name: 'Connect Buffer & share' })).toBeNull()
    expect(screen.getByRole('link', { name: "I don't use Buffer" })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Ship the thing')
  })

  test('open-link row without a card', async () => {
    mockApi({ verified: false, card: null })
    visit('/s/tok1')
    expect(await screen.findByRole('link', { name: 'Connect Buffer & share' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Verify/ })).toBeNull()
  })

  test('foreign', async () => {
    const calls = mockApi({ foreign: true })
    visit('/s/tok1?foreign=1')
    expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('This link was made for someone else')
    expect(screen.getByRole('link', { name: 'See the release' })).toHaveAttribute('href', base.launch.releaseUrl)
    expect(screen.queryByRole('link', { name: 'Connect Buffer & share' })).toBeNull()
    expect(calls[0]).toBe('GET /api/s/tok1?foreign=1')
  })
})

test('expired token shows dead end, never blank', async () => {
  mockApi({ status: 410, error: 'expired' })
  visit('/s/old')
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('This link has expired')
})

test('invalid and rate-limited tokens show a dead end', async () => {
  mockApi({ status: 404, error: 'not_found' })
  const { unmount } = visit('/s/nope')
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent("This link isn't valid")
  unmount()
  mockApi({ status: 429, error: 'rate_limited' })
  visit('/s/busy')
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent(
    'Too many opens from here, try again in a minute')
})

test('primary button visible at 375x667', async () => {
  // Design guard from the shipped CSS; Task 27's Playwright suite does the real layout check.
  const read = (f: string) => readFileSync(`src/${f}`, 'utf8')
  const tokens = read('styles/tokens.css')
  const crew = read('crew/crew.css')
  const comps = read('components/components.css')
  const body = (css: string, sel: string) => new RegExp(`${sel.replace('.', '\\.')}\\s*\\{([^}]*)\\}`).exec(css)![1]
  const px = (v: string) => {
    const t = new RegExp(`--${v}:\\s*([\\d.]+)(px|rem)`).exec(tokens)!
    return Number(t[1]) * (t[2] === 'rem' ? 16 : 1)
  }
  const num = (css: string, prop: string) => Number(new RegExp(`${prop}:\\s*([\\d.]+)`).exec(css)![1])
  const VIEWPORT = 375

  // Stack from the top of the page: shell padding, wordmark row, panel padding, card, perforation,
  // preview box, caption and the primary button, with the panel's gap between each block.
  const gutter = Math.min(Math.max(px('space-md'), VIEWPORT * 0.05), px('space-lg')) // clamp(md, 5vw, lg)
  const shellTop = px('space-lg')
  const markRow = 34 + px('space-ms') // wordmark mark is 2.125rem, margin below
  const panelPad = Math.min(Math.max(px('space-lg'), VIEWPORT * 0.06), px('space-2xl')) // clamp(lg, 6cqi, 2xl)
  const gap = px('space-md')
  const ratio = /aspect-ratio:\s*(\d+)\s*\/\s*(\d+)/.exec(body(comps, '.card'))!
  const card = (VIEWPORT - 2 * gutter - 2 * panelPad) * (Number(ratio[2]) / Number(ratio[1]))
  const perf = 22
  const prev = body(crew, '.crew-preview')
  const previewH =
    num(prev, '-webkit-line-clamp') * px('type-draft-size') * num(tokens, '--type-draft-leading') + 2 * px('space-md')
  expect(prev).toMatch(/padding:\s*var\(--space-md\)/)
  const cap = body(crew, '.crew-caption')
  const captionLines = 2 // trust strip wraps to two lines at 375px
  const captionH = captionLines * px('type-small-size') * num(tokens, '--type-small-leading')
  const button = Number(/min-height:\s*(\d+)px/.exec(body(comps, '.btn'))![1])
  expect(cap).toMatch(/margin:\s*0/)

  mockApi({ card: null })
  visit('/s/tok1')
  const primary = await screen.findByRole('link', { name: 'Connect Buffer & share' })
  const order = Array.from(document.querySelectorAll('h1, .crew-preview, .crew-caption, a.btn'))
  expect(order.map((n) => n.className || n.tagName)).toEqual([
    'card-title', 'crew-preview', 'crew-caption', 'btn btn-primary', 'btn btn-secondary',
  ])
  expect(order[3]).toBe(primary)
  const bottom = shellTop + markRow + panelPad + card + gap + perf + gap + previewH + gap + captionH + gap + button
  expect(bottom).toBeLessThanOrEqual(667)
})

test('fallback copy text equals neutral draft', async () => {
  const calls = mockApi({})
  const writeText = vi.fn(() => Promise.resolve())
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  visit('/s/tok1/fallback')
  const btn = await screen.findByRole('button', { name: 'Copy text' })
  await waitFor(() => expect(calls).toContain('POST /api/s/tok1/share'))
  vi.useFakeTimers({ shouldAdvanceTime: true })
  await act(async () => {
    fireEvent.click(btn)
  })
  expect(writeText).toHaveBeenCalledWith('Neutral draft text')
  expect(screen.getByRole('button', { name: 'Copied' })).toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent('Copied')
  act(() => vi.advanceTimersByTime(2100))
  expect(screen.getByRole('status')).toBeEmptyDOMElement()
  expect(screen.getByRole('button', { name: 'Copy text' })).toBeInTheDocument()

  const enc = encodeURIComponent('Neutral draft text')
  expect(screen.getByRole('link', { name: 'Open in X' })).toHaveAttribute('href', `https://twitter.com/intent/tweet?text=${enc}`)
  expect(screen.getByRole('link', { name: 'Open in Bluesky' })).toHaveAttribute('href', `https://bsky.app/intent/compose?text=${enc}`)
  expect(screen.getByRole('link', { name: 'Download card' })).toHaveAttribute('download')
  expect(screen.getByRole('link', { name: 'Create free Buffer account' })).toHaveAttribute(
    'href', '/auth/buffer/start?signup=1&return_to=/s/tok1/approve')
})

test('fallback shows select-all textarea when clipboard rejects', async () => {
  mockApi({ card: null })
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText: () => Promise.reject(new Error('denied')) }, configurable: true })
  visit('/s/tok1/fallback')
  await userEvent.click(await screen.findByRole('button', { name: 'Copy text' }))
  const area = await screen.findByRole('textbox')
  expect(area).toHaveValue('Neutral draft text')
  expect(area).toHaveAttribute('readonly')
  expect(screen.getByText('Select and copy', { selector: 'p' })).toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent('Select and copy')
  expect(area).toHaveFocus()
  expect(screen.queryByRole('link', { name: 'Download card' })).toBeNull()
})

test('foreign fallback is a dead end and never posts share', async () => {
  const calls = mockApi({ foreign: true })
  visit('/s/tok1/fallback?foreign=1')
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('This link was made for someone else')
  expect(screen.queryByRole('button')).toBeNull()
  expect(screen.queryByRole('link', { name: /Open in|Create free|Download/ })).toBeNull()
  expect(calls.some((c) => c.includes('/share'))).toBe(false)
})

test('linkedin intent is copy-first', async () => {
  mockApi({})
  visit('/s/tok1/fallback')
  await screen.findByRole('button', { name: 'Copy text' })
  expect(screen.getByText(/LinkedIn doesn't accept pre-filled text/)).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /LinkedIn/ })).toBeNull()
  const hrefs = screen.getAllByRole('link').map((a) => a.getAttribute('href') ?? '')
  expect(hrefs.some((h) => h.includes('linkedin'))).toBe(false)
})

test('renders without a launch time', async () => {
  mockApi({ launch: { ...base.launch, launchAt: null } })
  visit('/s/tok1')
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('Ship the thing')
})
