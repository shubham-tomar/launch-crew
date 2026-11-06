import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import App from '../App'
import type { CaptainSettings } from '../api'
import { CONSENT, renderComment, templateProblem } from './comment-template'

const DEFAULT =
  '**@{captain}** launched **{project} {version}** and credited {credited}.\n\n[Share your contribution]({link})\n\n' +
  CONSENT +
  '\n\nOpt out or delete your data: {settings}'

const settings = (over: Partial<CaptainSettings> = {}): CaptainSettings => ({
  commentTemplate: '',
  draftNotes: '',
  defaultComment: DEFAULT,
  placeholders: ['{captain}', '{project}', '{version}', '{credited}', '{link}', '{settings}'],
  preview: renderComment('', DEFAULT),
  maxCommentLen: 1500,
  maxNotesLen: 600,
  ...over,
})

type Call = { method: string; body?: unknown }

function mockApi(initial: CaptainSettings, reply?: (body: Record<string, string>) => Response) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((_url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method, body })
      if (method === 'PUT') {
        return Promise.resolve(
          reply ? reply(body) : new Response(JSON.stringify(settings({ ...body, preview: renderComment(body.commentTemplate ?? '', DEFAULT) })), { status: 200 }),
        )
      }
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

test('the page loads both templates, previews as you type and saves them', async () => {
  const calls = mockApi(settings())
  const user = userEvent.setup()
  visit()
  const box = await screen.findByLabelText('Comment template')
  expect(box).toHaveValue('')
  expect(screen.getByLabelText('Comment preview')).toHaveTextContent('**@you** launched **rocket v2.4.0**')
  expect(screen.getByRole('button', { name: 'Save templates' })).toBeDisabled()

  await user.type(box, 'Thanks for {{credited}!')
  expect(screen.getByText(/needs \{link\}/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Save templates' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: '{link}' }))
  expect(box).toHaveValue('Thanks for {credited}!{link}')
  expect(screen.getByLabelText('Comment preview')).toHaveTextContent('Thanks for your PR, *Split-pane resize with keyboard*!https://launchcrew.example/s/abc')
  expect(screen.getByLabelText('Comment preview')).toHaveTextContent(CONSENT)

  await user.type(screen.getByLabelText('Drafting notes'), 'Mention the Discord.')
  await user.click(screen.getByRole('button', { name: 'Save templates' }))
  expect(await screen.findByText('Saved.')).toBeInTheDocument()
  const put = calls.find((c) => c.method === 'PUT')
  expect(put?.body).toEqual({ commentTemplate: 'Thanks for {credited}!{link}', draftNotes: 'Mention the Discord.' })
})

test('a saved template can go back to the built-in one', async () => {
  mockApi(settings({ commentTemplate: 'Mine {link}' }))
  const user = userEvent.setup()
  visit()
  const box = await screen.findByLabelText('Comment template')
  expect(box).toHaveValue('Mine {link}')
  await user.click(screen.getByRole('button', { name: 'Use the built-in comment' }))
  expect(box).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Save templates' })).toBeEnabled()
})

test('a server refusal is shown beside the save button', async () => {
  mockApi(settings(), () => new Response(JSON.stringify({ error: 'invalid', message: 'keep it shorter' }), { status: 400 }))
  const user = userEvent.setup()
  visit()
  await user.type(await screen.findByLabelText('Drafting notes'), 'x')
  await user.click(screen.getByRole('button', { name: 'Save templates' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('keep it shorter')
})

test('Templates is in the captain nav', async () => {
  mockApi(settings())
  visit()
  expect(await screen.findByRole('link', { name: 'Templates' })).toHaveAttribute('aria-current', 'page')
})
