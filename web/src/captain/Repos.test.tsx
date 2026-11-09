import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import type { Repo } from '../api'
import Repos from './Repos'

const repo = (over: Partial<Repo> = {}): Repo => ({ id: 'r1', fullName: 'acme/rocket', enabled: true, allowPrerelease: false, logoUrl: '', accent: '', background: '', text: '', ...over })

function setup(list: Repo[]) {
  const calls: { method: string; url: string; body?: unknown }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, url, body })
      if (url.endsWith('/card-preview')) return Promise.resolve(new Response(JSON.stringify({ png: 'data:image/png;base64,AAAA' }), { status: 200 }))
      if (method === 'PATCH') return Promise.resolve(new Response(JSON.stringify({ ...list[0], ...body }), { status: 200 }))
      return Promise.resolve(new Response(JSON.stringify(list), { status: 200 }))
    }),
  )
  render(
    <MemoryRouter>
      <Repos />
    </MemoryRouter>,
  )
  return calls
}

test('Add a repo is the install link, in the header', async () => {
  setup([repo()])
  await screen.findByText('acme/rocket')
  expect(screen.getByRole('link', { name: 'Add a repo' })).toHaveAttribute('href', '/github/install/start')
  expect(screen.queryByRole('link', { name: /install the github app/i })).toBeNull()
})

test('each repo has two switches that call patchRepo', async () => {
  const calls = setup([repo()])
  const watch = await screen.findByRole('switch', { name: 'acme/rocket: watch releases' })
  const pre = screen.getByRole('switch', { name: 'acme/rocket: include prereleases' })
  expect(watch).toBeChecked()
  expect(pre).not.toBeChecked()

  await userEvent.click(pre)
  expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ allowPrerelease: true })
  await userEvent.click(watch)
  expect(calls.filter((c) => c.method === 'PATCH').at(-1)?.body).toEqual({ enabled: false })
})

test('no repos shows a friendly empty state with the add button', async () => {
  setup([])
  expect(await screen.findByRole('heading', { name: 'No repos yet' })).toBeInTheDocument()
  expect(screen.getAllByRole('link', { name: 'Add a repo' }).length).toBeGreaterThan(0)
  expect(screen.getByText(/creates a draft launch/)).toBeInTheDocument()
})

test('offline shows a plain message and Retry fetches again', async () => {
  const fetchMock = vi.fn()
    .mockRejectedValueOnce(new TypeError('Failed to fetch'))
    .mockResolvedValueOnce(new Response(JSON.stringify([repo()]), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  render(
    <MemoryRouter>
      <Repos />
    </MemoryRouter>,
  )
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not reach Launch Crew. Check your connection and try again.')
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('acme/rocket')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).toBeNull()
  expect(fetchMock).toHaveBeenCalledTimes(2)
})

test('a switch flips at once, is held while saving, and reverts with an inline error on failure', async () => {
  let release!: (r: Response) => void
  const list = [repo()]
  vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method === 'PATCH') return new Promise<Response>((res) => { release = res })
    return Promise.resolve(new Response(JSON.stringify(list), { status: 200 }))
  }))
  render(
    <MemoryRouter>
      <Repos />
    </MemoryRouter>,
  )
  const pre = await screen.findByRole('switch', { name: 'acme/rocket: include prereleases' })
  const watch = screen.getByRole('switch', { name: 'acme/rocket: watch releases' })
  await userEvent.click(pre)
  expect(pre).toBeChecked() // optimistic
  expect(pre).toBeDisabled()
  expect(watch).toBeDisabled() // the whole row waits
  expect(screen.getByText('Watching releases and prereleases')).toBeInTheDocument()

  release(new Response(JSON.stringify({ error: 'github_unavailable', message: 'GitHub is not answering' }), { status: 502 }))
  await screen.findByText(/GitHub is not answering/)
  expect(pre).not.toBeChecked()
  expect(pre).toBeEnabled()
  const row = pre.closest('li')!
  expect(within(row).getByRole('alert')).toHaveTextContent('GitHub is not answering')
  expect(screen.getByText('Watching releases')).toBeInTheDocument()
})

describe('card branding', () => {
  it('saves a logo and accent for the repo', async () => {
    const calls = setup([repo()])
    await userEvent.click(await screen.findByText('Card branding'))
    const form = screen.getByRole('form', { name: 'Card branding for acme/rocket' })
    expect(within(form).getByText(/acme's GitHub avatar/)).toBeInTheDocument()
    await userEvent.type(within(form).getByLabelText('Logo URL'), 'https://acme.dev/logo.png')
    await userEvent.click(within(form).getByRole('button', { name: 'Use #58D3AE for accent' }))
    await userEvent.click(within(form).getByRole('button', { name: 'Save branding' }))
    expect(await within(form).findByText(/New cards use it/)).toBeInTheDocument()
    expect(calls.at(-1)).toMatchObject({ method: 'PATCH', url: '/api/captain/repos/r1', body: { logoUrl: 'https://acme.dev/logo.png', accent: '#58D3AE' } })
  })

  it('refuses a malformed hex before sending', async () => {
    const calls = setup([repo()])
    await userEvent.click(await screen.findByText('Card branding'))
    await userEvent.type(screen.getByLabelText('Accent hex colour'), 'blue')
    expect(screen.getByText('Use a hex colour like #58D3AE.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save branding' })).toBeDisabled()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
  })

  it('saves a background and text colour beside the accent', async () => {
    const calls = setup([repo()])
    await userEvent.click(await screen.findByText('Card branding'))
    const form = screen.getByRole('form', { name: 'Card branding for acme/rocket' })
    await userEvent.click(within(form).getByRole('button', { name: 'Use #F4EFE6 for background' }))
    await userEvent.type(within(form).getByLabelText('Text hex colour'), '#4A45D1')
    await userEvent.click(within(form).getByRole('button', { name: 'Save branding' }))
    expect(await within(form).findByText(/New cards use it/)).toBeInTheDocument()
    expect(calls.at(-1)).toMatchObject({ method: 'PATCH', body: { background: '#F4EFE6', text: '#4A45D1', accent: '' } })
  })
})

test('the branding fold draws a preview once open, and again when the accent changes', async () => {
  const calls = setup([repo()])
  await screen.findByText('acme/rocket')
  const previews = () => calls.filter((c) => c.url.endsWith('/card-preview'))
  expect(previews()).toHaveLength(0) // closed: nothing drawn
  await userEvent.click(screen.getByText('Card branding'))
  const img = await screen.findByRole('img', { name: 'Sample card for acme/rocket with this branding' }, { timeout: 2000 })
  expect(img).toHaveAttribute('src', 'data:image/png;base64,AAAA')
  expect(previews()[0]).toMatchObject({ method: 'POST', url: '/api/captain/repos/r1/card-preview', body: { logoUrl: '', accent: '', background: '', text: '' } })
  await userEvent.click(screen.getByRole('button', { name: 'Use #D9A520 for accent' }))
  await waitFor(() => expect(previews().at(-1)?.body).toEqual({ logoUrl: '', accent: '#D9A520', background: '', text: '' }), { timeout: 2000 })
})
