import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import App from '../App'
import { logoOf, safeBack } from './Show'

const toCanvas = vi.hoisted(() => vi.fn(() => Promise.resolve()))
vi.mock('qrcode', () => ({ default: { toCanvas } }))

afterEach(() => vi.unstubAllGlobals())

function visit(path = '/l/open1/show') {
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  )
}

test('show page: no chrome, QR of the short link, download link', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({ title: 'GoMeetup Nov', shortUrl: 'https://lc.test/l/open1' }),
        ),
      ),
    ),
  )
  render(
    <MemoryRouter initialEntries={['/l/open1/show']}>
      <App />
    </MemoryRouter>,
  )
  expect(await screen.findByRole('heading', { level: 1 })).toHaveTextContent('GoMeetup Nov')
  expect(screen.getByText('https://lc.test/l/open1')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Download PNG' })).toHaveAttribute('href', '/api/l/open1/show.png')
  expect(screen.queryByRole('radiogroup')).toBeNull()
  // The QR is drawn in an effect after the page renders; on a slow runner that can land a tick later.
  await waitFor(() =>
    expect(toCanvas).toHaveBeenCalledWith(expect.anything(), 'https://lc.test/l/open1', expect.objectContaining({ errorCorrectionLevel: 'H' })),
  )
})

test('show page uses the show endpoint and notes a failed QR draw', async () => {
  toCanvas.mockRejectedValueOnce(new Error('boom'))
  const f = vi.fn(() => Promise.resolve(new Response(JSON.stringify({ title: 'T', shortUrl: 'https://lc.test/l/open1' }))))
  vi.stubGlobal('fetch', f)
  visit()
  expect(await screen.findByText(/QR code could not be drawn/)).toBeInTheDocument()
  expect(f).toHaveBeenCalledWith('/api/l/open1/show', undefined)
})

test('the QR centre is the plane unless the captain chose otherwise', () => {
  expect(logoOf({})).toEqual({ kind: 'plane' })
  expect(logoOf({ qrLogo: 'plane' })).toEqual({ kind: 'plane' })
  expect(logoOf({ qrLogo: 'none' })).toEqual({ kind: 'none' })
  expect(logoOf({ qrLogo: 'custom', qrLogoUrl: 'https://cdn.test/logo.png' })).toEqual({ kind: 'image', url: 'https://cdn.test/logo.png' })
  // a custom logo with no address cannot be drawn: keep the plane
  expect(logoOf({ qrLogo: 'custom', qrLogoUrl: '' })).toEqual({ kind: 'plane' })
})

test('the QR colours come from the design tokens, not literals', async () => {
  const tokens: Record<string, string> = { '--color-card-ink': '#111111', '--color-slide-text': '#eeeeee', '--color-primary': '#222222' }
  vi.stubGlobal('getComputedStyle', () => ({ getPropertyValue: (k: string) => tokens[k] ?? '' }))
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify({ title: 'T', shortUrl: 'https://lc.test/l/open1' })))))
  const fill: string[] = []
  const ctx = {
    save() {}, restore() {}, translate() {}, scale() {}, rotate() {}, beginPath() {}, moveTo() {}, arcTo() {}, closePath() {},
    fill() { fill.push(String(this.fillStyle)) }, fillStyle: '',
  }
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(ctx as unknown as CanvasRenderingContext2D)
  vi.stubGlobal('Path2D', class {})
  visit()
  await screen.findByRole('heading', { level: 1 })
  await vi.waitFor(() => expect(toCanvas).toHaveBeenLastCalledWith(expect.anything(), 'https://lc.test/l/open1',
    expect.objectContaining({ color: { dark: '#111111', light: '#eeeeee' } })))
  await vi.waitFor(() => expect(fill).toEqual(['#eeeeee', '#222222']))
  vi.restoreAllMocks()
})

test('the slide has a Close control that leaves it, and Escape does the same', async () => {
  const f = vi.fn((url: string) =>
    Promise.resolve(new Response(JSON.stringify(url.includes('/show') ? { title: 'T', shortUrl: 'https://lc.test/l/open1' } : []))),
  )
  vi.stubGlobal('fetch', f)
  visit()
  await screen.findByRole('heading', { level: 1 })
  // Opened fresh, there is no page to go back to, so Close goes to the launches.
  await userEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(await screen.findByText(/No launches yet/)).toBeInTheDocument()
  expect(f).toHaveBeenCalledWith('/api/captain/launches', undefined)
})

test('Escape leaves the slide too', async () => {
  const f = vi.fn((url: string) =>
    Promise.resolve(new Response(JSON.stringify(url.includes('/show') ? { title: 'T', shortUrl: 'https://lc.test/l/open1' } : []))),
  )
  vi.stubGlobal('fetch', f)
  visit()
  await screen.findByRole('heading', { level: 1 })
  await userEvent.keyboard('{Escape}')
  expect(await screen.findByText(/No launches yet/)).toBeInTheDocument()
})

test('Close returns to the launch named in ?back=, which must be a launch page', async () => {
  const f = vi.fn((url: string) =>
    Promise.resolve(
      url.includes('/show')
        ? new Response(JSON.stringify({ title: 'T', shortUrl: 'https://lc.test/l/open1' }))
        : new Response(JSON.stringify({ error: 'not_found', message: 'gone' }), { status: 404 }),
    ),
  )
  vi.stubGlobal('fetch', f)
  visit('/l/open1/show?back=%2Fcaptain%2Flaunches%2Fl1')
  await screen.findByRole('heading', { level: 1 })
  await userEvent.click(screen.getByRole('button', { name: 'Close' }))
  await vi.waitFor(() => expect(f).toHaveBeenCalledWith('/api/captain/launches/l1', undefined))
  expect(safeBack('/captain/launches/l1')).toBe('/captain/launches/l1')
  for (const bad of ['https://evil.test', '//evil.test', '/captain/launches/l1/../../x', '/settings', null]) {
    expect(safeBack(bad)).toBeNull()
  }
})
