import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import App from '../App'
import type { Me } from '../api'

afterEach(() => vi.unstubAllGlobals())

const me = (o: Partial<Me> = {}): Me => ({
  signedIn: true, githubLogins: [], bufferConnected: false, optedOut: false, launches: 0, crewRows: 0, supportEmail: 'help@lc.test', ...o,
})

function mockApi(m: Me, opts: { deleteFails?: boolean; optOut?: Response; tokenScope?: 'launch' | 'all' } = {}) {
  const calls: { method: string; url: string }[] = []
  const json = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ method, url })
    if (url === '/api/me' && method === 'GET') return json(200, m)
    if (url === '/api/me' && method === 'DELETE') return opts.deleteFails ? json(500, { error: 'internal' }) : json(200, { deleted: true })
    if (url === '/api/me/opt-out' && method === 'DELETE') return json(200, { optedOut: false })
    if (url.startsWith('/api/me/opt-out') && opts.optOut) return Promise.resolve(opts.optOut)
    if (url.startsWith('/api/me/opt-out')) return json(200, { optedOut: true, scope: 'all' })
    if (url.startsWith('/api/opt-out')) return json(200, { optedOut: true, scope: opts.tokenScope ?? 'launch' })
    return json(404, { error: 'not_found' })
  }))
  return calls
}

const everywhere = 'Opted out. We will never comment on your PRs again.'
const thisLaunch = 'Opted out. We will not comment on your PRs for this launch.'

function visit(path = '/settings') {
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  )
}

test('settings states render', async () => {
  // Empty.
  mockApi(me())
  visit()
  expect(await screen.findByText('Nothing stored about you yet.')).toBeInTheDocument()
})

test('settings states render: stored, opted out, deleted', async () => {
  const user = userEvent.setup()
  const calls = mockApi(me({ githubLogins: ['ada'], bufferConnected: true, launches: 1, crewRows: 2 }))
  visit()
  expect(await screen.findByText('GitHub: ada')).toBeInTheDocument()
  expect(screen.queryByText('Nothing stored about you yet.')).toBeNull()

  // Opting out needs a second press, and can be undone while signed in.
  await user.click(screen.getByRole('button', { name: 'Opt out of all comments' }))
  expect(calls.some((c) => c.url === '/api/me/opt-out')).toBe(false)
  await user.click(screen.getByRole('button', { name: 'Yes, opt out' }))
  expect(await screen.findByText(everywhere)).toBeInTheDocument()
  expect(calls).toContainEqual({ method: 'POST', url: '/api/me/opt-out' })
  await user.click(screen.getByRole('button', { name: 'Undo opt-out' }))
  expect(await screen.findByText('Opt-out undone. Future launches can invite you again.')).toBeInTheDocument()
  expect(calls).toContainEqual({ method: 'DELETE', url: '/api/me/opt-out' })
  expect(screen.queryByText(everywhere)).toBeNull()
  expect(screen.getByRole('button', { name: 'Opt out of all comments' })).toBeInTheDocument()

  // Delete needs a second press.
  await user.click(screen.getByRole('button', { name: 'Delete everything' }))
  expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/me')).toBe(false)
  await user.click(screen.getByRole('button', { name: 'Yes, delete everything' }))
  expect(await screen.findByText('Deleted. Your tokens and drafts are gone.')).toBeInTheDocument()
  expect(calls).toContainEqual({ method: 'DELETE', url: '/api/me' })
})

test('settings states render: delete failure shows the support address as text', async () => {
  const user = userEvent.setup()
  mockApi(me({ githubLogins: ['ada'] }), { deleteFails: true })
  visit()
  await user.click(await screen.findByRole('button', { name: 'Delete everything' }))
  await user.click(screen.getByRole('button', { name: 'Yes, delete everything' }))
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('We could not delete everything; email us at help@lc.test')
  expect(screen.queryByRole('link', { name: /help@lc.test/ })).toBeNull()
})

test('a token holder who is signed out opts out of this launch and is told how to go further', async () => {
  const user = userEvent.setup()
  const calls = mockApi(me({ signedIn: false, supportEmail: '' }))
  visit('/opt-out?token=abc%2F1')
  const btn = await screen.findByRole('button', { name: 'Opt out of comments for this launch' })
  expect(screen.queryByRole('button', { name: 'Delete everything' })).toBeNull()
  expect(screen.queryByText('Nothing stored about you yet.')).toBeNull()
  await user.click(btn)
  expect(calls.some((c) => c.url.startsWith('/api/opt-out'))).toBe(false)
  expect(screen.getByText(/stops comments for this launch only/)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Yes, opt out' }))
  expect(await screen.findByText(thisLaunch, { exact: false })).toBeInTheDocument()
  expect(calls).toContainEqual({ method: 'POST', url: '/api/opt-out?token=abc%2F1' })
  // The way to stop comments everywhere is the GitHub verify step on the invite.
  expect(screen.getByRole('link', { name: 'your invite link' })).toHaveAttribute('href', '/s/abc%2F1')
  expect(screen.queryByRole('button', { name: 'Undo opt-out' })).toBeNull()
})

test('signed out without a token is asked to sign in', async () => {
  mockApi(me({ signedIn: false }))
  visit()
  expect(await screen.findByRole('link', { name: 'Sign in to see what we store' })).toHaveAttribute('href', expect.stringContaining('/auth/buffer/start'))
})

test('a signed-in visitor with a comment token opts out through the token', async () => {
  const user = userEvent.setup()
  const calls = mockApi(me({ githubLogins: ['ada'] }), { tokenScope: 'all' })
  visit('/settings?token=abc')
  await user.click(await screen.findByRole('button', { name: 'Opt out of comments for this launch' }))
  await user.click(screen.getByRole('button', { name: 'Yes, opt out' }))
  // The server knew the token's GitHub account is theirs, so the opt-out went everywhere.
  expect(await screen.findByText(everywhere)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Undo opt-out' })).toBeInTheDocument()
  expect(calls).toContainEqual({ method: 'POST', url: '/api/opt-out?token=abc' })
  expect(calls.some((c) => c.url === '/api/me/opt-out')).toBe(false)
})

test('a signed-in visitor whose token names another GitHub account is told the opt-out covers this launch', async () => {
  const user = userEvent.setup()
  mockApi(me({ githubLogins: ['ada'] }))
  visit('/settings?token=abc')
  await user.click(await screen.findByRole('button', { name: 'Opt out of comments for this launch' }))
  await user.click(screen.getByRole('button', { name: 'Yes, opt out' }))
  expect(await screen.findByText(thisLaunch, { exact: false })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Undo opt-out' })).toBeNull()
})

test('a signed-in person who is already opted out can undo it', async () => {
  const user = userEvent.setup()
  const calls = mockApi(me({ githubLogins: ['ada'], optedOut: true }))
  visit()
  expect(await screen.findByText(everywhere)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Undo opt-out' }))
  expect(await screen.findByText('Opt-out undone. Future launches can invite you again.')).toBeInTheDocument()
  expect(calls).toContainEqual({ method: 'DELETE', url: '/api/me/opt-out' })
})

test('opt-out with no linked GitHub account says so instead of succeeding', async () => {
  const user = userEvent.setup()
  mockApi(me({ githubLogins: [], bufferConnected: true }), {
    optOut: new Response(JSON.stringify({ error: 'no_github' }), { status: 409 }),
  })
  visit()
  await user.click(await screen.findByRole('button', { name: 'Opt out of all comments' }))
  await user.click(screen.getByRole('button', { name: 'Yes, opt out' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('We found no GitHub account to opt out. Use the link from a PR comment.')
  expect(screen.queryByText(everywhere)).toBeNull()
})

describe('how a captain hears about drafts', () => {
  const notify = { github: true, releaseNotes: false, email: false, emailAddress: '', emailPending: '', mailAvailable: true }

  function mockNotify(m: Me, answers: Record<string, (body: unknown) => [number, unknown]>) {
    const sent: { method: string; url: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      sent.push({ method, url, body })
      const res = (status: number, b: unknown) => Promise.resolve(new Response(JSON.stringify(b), { status }))
      if (url === '/api/me' && method === 'GET') return res(200, m)
      const a = answers[`${method} ${url}`]
      return a ? res(...a(body)) : res(404, { error: 'not_found' })
    }))
    return sent
  }

  test('crew without repos see no notification choices', async () => {
    mockNotify(me({ crewRows: 1 }), {})
    visit()
    await screen.findByText(/crew invite/)
    expect(screen.queryByRole('heading', { name: 'When a release makes a draft' })).toBeNull()
  })

  test('GitHub is on by default and a change saves at once', async () => {
    const sent = mockNotify(me({ launches: 1, notify }), {
      'PUT /api/me/notify': (b) => [200, { ...notify, ...(b as object) }],
    })
    visit()
    const gh = await screen.findByRole('checkbox', { name: /@mention me on GitHub/ })
    expect(gh).toBeChecked()
    const notes = screen.getByRole('checkbox', { name: /Add a link to the release notes/ })
    expect(notes).not.toBeChecked()
    await userEvent.click(notes)
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
    expect(notes).toBeChecked()
    expect(sent.find((c) => c.method === 'PUT')?.body).toEqual({ github: true, releaseNotes: true, email: false })
  })

  test('email asks for an address and sends a link before it turns on', async () => {
    const sent = mockNotify(me({ launches: 1, notify }), {
      'POST /api/me/notify/email': (b) => [200, { ...notify, emailPending: (b as { address: string }).address }],
    })
    visit()
    await userEvent.click(await screen.findByRole('checkbox', { name: /Email me/ }))
    await userEvent.type(screen.getByLabelText('Your email'), 'cap@lc.test')
    await userEvent.click(screen.getByRole('button', { name: 'Send link' }))
    expect(await screen.findByText(/We sent a link to cap@lc.test/)).toBeInTheDocument()
    expect(sent.some((c) => c.method === 'PUT')).toBe(false)
  })

  test('a verified address can be turned off, and the link result is shown', async () => {
    const on = { ...notify, email: true, emailAddress: 'cap@lc.test' }
    mockNotify(me({ launches: 1, notify: on }), {
      'PUT /api/me/notify': (b) => [200, { ...on, ...(b as object) }],
    })
    visit('/settings?email=verified')
    expect(await screen.findByText(/Email confirmed/)).toBeInTheDocument()
    const email = screen.getByRole('checkbox', { name: /Email me/ })
    expect(email).toBeChecked()
    expect(screen.getByText('To cap@lc.test.')).toBeInTheDocument()
    await userEvent.click(email)
    await vi.waitFor(() => expect(email).not.toBeChecked())
  })

  test('without a mail provider email is shown but cannot be chosen', async () => {
    mockNotify(me({ launches: 1, notify: { ...notify, mailAvailable: false } }), {})
    visit()
    expect(await screen.findByRole('checkbox', { name: /Email me/ })).toBeDisabled()
    expect(screen.getByText('Email is not set up on this server yet.')).toBeInTheDocument()
  })
})
