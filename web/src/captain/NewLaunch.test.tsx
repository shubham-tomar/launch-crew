import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import NewLaunch from './NewLaunch'
import App from '../App'
import type { Detail } from '../api'
import { pngFile, stubXhr } from '../test/xhr'

type Call = { method: string; url: string; body?: Record<string, unknown> }
const isCreate = (c: Call) => c.method === 'POST' && c.url.endsWith('/launches')

function setup() {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      if (url.endsWith('/templates')) {
        return Promise.resolve(new Response(JSON.stringify({ templates: [{ id: 't1', name: 'Meetups', commentTemplate: '', draftNotes: '', preview: '' }] }), { status: 200 }))
      }
      if (url.endsWith('/qr-preview')) return Promise.resolve(new Response(JSON.stringify({ png: 'data:image/png;base64,QR' }), { status: 200 }))
      return Promise.resolve(new Response(JSON.stringify({ id: 'new1' }), { status: 201 }))
    }),
  )
  render(
    <MemoryRouter>
      <NewLaunch />
    </MemoryRouter>,
  )
  return calls
}

test('asks where the launch comes from, meetup selected by default', () => {
  setup()
  expect(screen.getByText('Where does this come from?')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Meetup or talk' })).toHaveAttribute('aria-pressed', 'true')
  expect(screen.getByRole('button', { name: 'GitHub release' })).toHaveAttribute('aria-pressed', 'false')
})

test('GitHub release shows the explainer and no form to submit', async () => {
  setup()
  await userEvent.click(screen.getByRole('button', { name: 'GitHub release' }))
  expect(screen.getByText('Launch Crew starts this on its own.')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Add a repo' })).toHaveAttribute('href', '/captain/repos')
  expect(screen.getByRole('link', { name: 'See your repos' })).toHaveAttribute('href', '/captain/repos')
  expect(screen.queryByRole('button', { name: 'Create launch' })).toBeNull()
  expect(screen.queryByLabelText('Title')).toBeNull()
})

test('Meetup prefills the three usual roles; Manual post starts bare', async () => {
  setup()
  const names = () => screen.queryAllByLabelText(/^Role \d name$/).map((e) => (e as HTMLInputElement).value)
  expect(names()).toEqual(['Attendee', 'Speaker', 'Organiser'])
  await userEvent.click(screen.getByRole('button', { name: 'Manual post' }))
  expect(names()).toEqual([])
  await userEvent.click(screen.getByRole('button', { name: 'Meetup or talk' }))
  expect(names()).toEqual(['Attendee', 'Speaker', 'Organiser'])
})

test('the default QR mark is the plane and sends nothing extra', async () => {
  const calls = setup()
  expect(screen.getByRole('radio', { name: 'Plane' })).toBeChecked()
  await userEvent.type(screen.getByLabelText('Title'), 'Go night')
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  const post = calls.find((c) => isCreate(c))
  expect(post?.body).toBeDefined()
  expect(post?.body).not.toHaveProperty('qrLogo')
  expect(post?.body).not.toHaveProperty('qrLogoUrl')
})

test('choosing None sends qrLogo none', async () => {
  const calls = setup()
  await userEvent.click(screen.getByRole('radio', { name: 'None' }))
  await userEvent.type(screen.getByLabelText('Title'), 'Go night')
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(calls.find((c) => isCreate(c))?.body).toMatchObject({ qrLogo: 'none' })
})

test('my own image needs an https URL, then sends qrLogo custom with it', async () => {
  const calls = setup()
  await userEvent.click(screen.getByRole('radio', { name: 'My own image' }))
  await userEvent.type(screen.getByLabelText('Title'), 'Go night')
  await userEvent.type(screen.getByLabelText('QR mark image URL'), 'http://x.test/a.png')
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(await screen.findByText('Use an https:// image URL.')).toBeInTheDocument()
  expect(calls.some((c) => isCreate(c))).toBe(false)

  const url = screen.getByLabelText('QR mark image URL')
  await userEvent.clear(url)
  await userEvent.type(url, 'https://x.test/logo.png')
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(calls.find((c) => isCreate(c))?.body).toMatchObject({ qrLogo: 'custom', qrLogoUrl: 'https://x.test/logo.png' })
})

test('the window and launch time follow the same rules as the launch page, with the message by the field', async () => {
  const calls = setup()
  await userEvent.type(screen.getByLabelText('Title'), 'Go night')
  const win = screen.getByLabelText('Window (minutes)')
  expect(win).toHaveAttribute('min', '5')
  expect(win).toHaveAttribute('max', '720')
  await userEvent.clear(win)
  await userEvent.type(win, '2')
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(calls.some((c) => isCreate(c))).toBe(false)
  expect(win).toHaveAttribute('aria-invalid', 'true')
  const winMsg = screen.getByText('The window is 5 to 720 minutes.')
  expect(win.getAttribute('aria-describedby')).toContain(winMsg.id)

  await userEvent.clear(win)
  await userEvent.type(win, '90')
  const { fireEvent } = await import('@testing-library/react')
  const at = screen.getByLabelText('Launch time')
  fireEvent.change(at, { target: { value: '2020-01-01T10:00' } })
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(calls.some((c) => isCreate(c))).toBe(false)
  expect(win).not.toHaveAttribute('aria-invalid')
  expect(at).toHaveAttribute('aria-invalid', 'true')
  const atMsg = screen.getByText('Pick a time in the future.')
  expect(at.getAttribute('aria-describedby')).toContain(atMsg.id)

  fireEvent.change(at, { target: { value: '' } })
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(calls.find((c) => isCreate(c))?.body).toMatchObject({ title: 'Go night', windowMinutes: 90 })
})

test('an empty title is refused with the message by the field', async () => {
  const calls = setup()
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(calls.some((c) => isCreate(c))).toBe(false)
  const title = screen.getByLabelText('Title')
  expect(title).toHaveAttribute('aria-invalid', 'true')
  expect(title.getAttribute('aria-describedby')).toContain(screen.getByText('Give the launch a title.').id)
})

test('the event slide preview draws the title and QR mark', async () => {
  const calls = setup()
  await userEvent.type(screen.getByLabelText('Title'), 'Go night')
  const img = await screen.findByRole('img', { name: /Sample event slide for Go night/ }, { timeout: 2000 })
  expect(img).toHaveAttribute('src', 'data:image/png;base64,QR')
  const previews = calls.filter((c) => c.url.endsWith('/qr-preview'))
  expect(previews.at(-1)?.body).toEqual({ title: 'Go night', qrLogo: 'plane' })
})

test('a hero URL shows a preview, and a broken one says so', async () => {
  setup()
  expect(screen.queryByAltText('Hero image preview')).toBeNull()
  await userEvent.type(screen.getByLabelText('Hero image URL (optional)'), 'https://x.test/hero.jpg')
  const img = screen.getByAltText('Hero image preview')
  expect(img).toHaveAttribute('src', 'https://x.test/hero.jpg')
  img.dispatchEvent(new Event('error'))
  expect(await screen.findByText(/did not load as an image/)).toBeInTheDocument()
})

test('a picked template is sent with the launch; the built-in sends none', async () => {
  const calls = setup()
  const select = screen.getByRole('combobox', { name: 'Template' })
  await userEvent.type(screen.getByLabelText('Title'), 'Go night')
  await waitFor(() => expect(screen.getAllByRole('option', { hidden: true })).toHaveLength(2))
  await userEvent.click(select)
  await userEvent.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveTextContent('Meetups')
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(calls.find(isCreate)?.body).toMatchObject({ title: 'Go night', templateId: 't1' })
})

// Images picked on New launch go up after the launch exists. One that fails must not
// lose the launch: the captain lands on its page and the failed file is named there.
test('a failed upload after create still opens the launch and names the file', async () => {
  const sent = stubXhr([
    { status: 201, body: { id: 'i1', url: 'https://cdn.example/uploads/1.jpg' } },
    { status: 400, body: { error: 'not_image', message: 'Use a JPEG, PNG, GIF or WebP image.' } },
  ])
  const created: Detail = {
    id: 'new1', status: 'draft', source: 'manual', title: 'Go night', summary: '', releaseTag: '', releaseUrl: '', launchAt: null,
    windowMinutes: 90, crewCap: 200, openCount: 0, openUrl: '', showUrl: '', pngUrl: '', roles: [], bufferConnected: true,
    headlines: [], chores: [], captainDrafts: [], crew: [], checks: [], deadLetters: [], counts: { in: 0, total: 0 },
    captainName: 'You', qrLogo: 'plane', qrLogoUrl: '', images: [{ id: 'i1', url: 'https://cdn.example/uploads/1.jpg' }], includeCard: true,
  }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const ok = (b: unknown, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s }))
      if (url.endsWith('/templates')) return ok({ templates: [] })
      if (url.endsWith('/qr-preview')) return ok({ png: 'data:image/png;base64,QR' })
      if (url === '/api/captain/launches' && init?.method === 'POST') return ok(created, 201)
      return ok(created)
    }),
  )
  render(
    <MemoryRouter initialEntries={['/captain/new']}>
      <App />
    </MemoryRouter>,
  )
  await userEvent.type(await screen.findByLabelText('Title'), 'Go night')
  fireEvent.change(screen.getByLabelText('Add images'), { target: { files: [pngFile('one.png'), pngFile('two.heic.png')] } })
  expect(screen.getByRole('img', { name: 'Image 2' })).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Create launch' }))
  expect(await screen.findByRole('heading', { level: 1, name: 'Go night' })).toBeInTheDocument()
  expect(sent.map((s) => s.file.name)).toEqual(['one.png', 'two.heic.png'])
  expect(sent[0].url).toBe('/api/captain/launches/new1/images')
  expect(screen.getByText('two.heic.png')).toBeInTheDocument()
  expect(screen.getByText('Use a JPEG, PNG, GIF or WebP image.')).toBeInTheDocument()
  expect(screen.getByRole('img', { name: 'Image 1' })).toHaveAttribute('src', 'https://cdn.example/uploads/1.jpg')
})
