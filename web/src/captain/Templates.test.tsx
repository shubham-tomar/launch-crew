import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import App from '../App'
import type { Template, TemplatesView } from '../api'
import { CONSENT, renderComment, templateProblem } from './comment-template'

const DEFAULT =
  '**@{captain}** launched **{project} {version}** and credited {credited}.\n\n[Share your contribution]({link})\n\n' +
  CONSENT +
  '\n\nOpt out or delete your data: {settings}'

const view = (templates: Template[] = []): TemplatesView => ({
  templates,
  defaultComment: DEFAULT,
  defaultPreview: renderComment('', DEFAULT),
  placeholders: ['{captain}', '{project}', '{version}', '{credited}', '{link}', '{settings}'],
  maxCommentLen: 1500,
  maxNotesLen: 600,
  maxNameLen: 40,
  maxTemplates: 20,
})

const tmpl = (over: Partial<Template> = {}): Template => ({
  id: 't1',
  name: 'Meetups',
  commentTemplate: '',
  draftNotes: '',
  preview: renderComment('', DEFAULT),
  ...over,
})

type Call = { method: string; url: string; body?: unknown }

function mockApi(initial: TemplatesView, reply?: (body: Record<string, string>) => Response) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, url, body })
      if (method === 'POST' || method === 'PUT') {
        const id = method === 'POST' ? 'new1' : url.split('/').pop()
        return Promise.resolve(
          reply ? reply(body) : new Response(JSON.stringify(tmpl({ ...body, id, preview: renderComment(body.commentTemplate ?? '', DEFAULT) })), { status: method === 'POST' ? 201 : 200 }),
        )
      }
      if (method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      return Promise.resolve(new Response(JSON.stringify(initial), { status: 200 }))
    }),
  )
  return calls
}

function visit() {
  return render(
    <MemoryRouter initialEntries={['/captain/templates']}>
      <App />
    </MemoryRouter>,
  )
}

afterEach(() => vi.unstubAllGlobals())

test('renderComment fills placeholders and always keeps the consent line and the opt-out link', () => {
  const got = renderComment('Hey {captain}, {project} {version} is out thanks to {credited}. Tap {link}', DEFAULT)
  expect(got).toContain('Hey you, rocket v2.4.0 is out thanks to your PR')
  expect(got).toContain('Tap https://launchcrew.example/s/abc')
  expect(got).toContain(CONSENT)
  expect(got).toContain('Opt out or delete your data: https://launchcrew.example/settings')
  // The default renders without doubling its own footer.
  expect(renderComment('', DEFAULT).split(CONSENT)).toHaveLength(2)
})

test('templateProblem names the missing link and the length cap; empty is fine', () => {
  expect(templateProblem('', 100)).toBe('')
  expect(templateProblem('Thanks, tap {link}', 100)).toBe('')
  expect(templateProblem('Thanks', 100)).toMatch(/\{link\}/)
  expect(templateProblem('x'.repeat(101) + '{link}', 100)).toMatch(/under 100/)
})

test('the built-in template is shown read-only first', async () => {
  mockApi(view([tmpl()]))
  visit()
  expect(await screen.findByRole('button', { name: 'Built-in' })).toHaveAttribute('aria-pressed', 'true')
  expect(screen.getByLabelText('Comment preview')).toHaveTextContent('**@you** launched **rocket v2.4.0**')
  expect(screen.queryByLabelText('Comment template')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Meetups' })).toBeInTheDocument()
})

test('a new template previews as you type and is created with its name', async () => {
  const calls = mockApi(view())
  const user = userEvent.setup()
  visit()
  await user.click(await screen.findByRole('button', { name: '+ New template' }))
  const create = screen.getByRole('button', { name: 'Create template' })
  expect(create).toBeDisabled() // no name yet
  await user.type(screen.getByLabelText('Template name'), 'Meetups')
  const box = screen.getByLabelText('Comment template')
  await user.type(box, 'Thanks for {{credited}!')
  expect(screen.getByText(/needs \{link\}/)).toBeInTheDocument()
  expect(create).toBeDisabled()
  await user.click(screen.getByRole('button', { name: '{link}' }))
  expect(box).toHaveValue('Thanks for {credited}!{link}')
  expect(screen.getByLabelText('Comment preview')).toHaveTextContent('Thanks for your PR, *Split-pane resize with keyboard*!https://launchcrew.example/s/abc')
  expect(screen.getByLabelText('Comment preview')).toHaveTextContent(CONSENT)
  await user.type(screen.getByLabelText('Drafting notes'), 'Mention the Discord.')
  await user.click(create)
  expect(await screen.findByText('Saved.')).toBeInTheDocument()
  expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'Meetups', commentTemplate: 'Thanks for {credited}!{link}', draftNotes: 'Mention the Discord.' })
  expect(screen.getByRole('button', { name: 'Meetups' })).toHaveAttribute('aria-pressed', 'true')
})

test('an existing template saves in place and can go back to the built-in comment', async () => {
  const calls = mockApi(view([tmpl({ commentTemplate: 'Mine {link}' })]))
  const user = userEvent.setup()
  visit()
  await user.click(await screen.findByRole('button', { name: 'Meetups' }))
  const box = screen.getByLabelText('Comment template')
  expect(box).toHaveValue('Mine {link}')
  expect(screen.getByRole('button', { name: 'Save template' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Use the built-in comment' }))
  expect(box).toHaveValue('')
  await user.click(screen.getByRole('button', { name: 'Save template' }))
  expect(await screen.findByText('Saved.')).toBeInTheDocument()
  expect(calls.find((c) => c.method === 'PUT')).toMatchObject({ url: '/api/captain/templates/t1', body: { name: 'Meetups', commentTemplate: '' } })
})

test('deleting asks first, then drops the template', async () => {
  const calls = mockApi(view([tmpl()]))
  const user = userEvent.setup()
  visit()
  await user.click(await screen.findByRole('button', { name: 'Meetups' }))
  await user.click(screen.getByRole('button', { name: 'Delete' }))
  expect(screen.getByText(/go back to the built-in template/)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Delete template' }))
  expect(await screen.findByRole('button', { name: 'Built-in' })).toHaveAttribute('aria-pressed', 'true')
  expect(screen.queryByRole('button', { name: 'Meetups' })).not.toBeInTheDocument()
  expect(calls.some((c) => c.method === 'DELETE' && c.url === '/api/captain/templates/t1')).toBe(true)
})

test('a server refusal is shown beside the save button', async () => {
  mockApi(view(), () => new Response(JSON.stringify({ error: 'invalid', message: 'you already have a template called "Meetups"' }), { status: 400 }))
  const user = userEvent.setup()
  visit()
  await user.click(await screen.findByRole('button', { name: '+ New template' }))
  await user.type(screen.getByLabelText('Template name'), 'Meetups')
  await user.click(screen.getByRole('button', { name: 'Create template' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('already have a template')
})

test('Templates is in the captain nav', async () => {
  mockApi(view())
  visit()
  expect(await screen.findByRole('link', { name: 'Templates' })).toHaveAttribute('aria-current', 'page')
})
