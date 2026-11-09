import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import App from './App'

function renderHome() {
  render(
    <MemoryRouter initialEntries={['/']}>
      <App />
    </MemoryRouter>,
  )
}

test('home offers sign in and the settings link', () => {
  renderHome()
  expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('One launch. Nine people. Nine posts, all theirs.')
  const signIns = screen.getAllByRole('link', { name: 'Sign in with Buffer' })
  expect(signIns.length).toBeGreaterThanOrEqual(2)
  for (const a of signIns) expect(a).toHaveAttribute('href', '/auth/buffer/start?return_to=/captain')
  expect(screen.getByRole('link', { name: 'Start a launch' })).toHaveAttribute('href', '/auth/buffer/start?return_to=/captain')
  expect(screen.getByRole('link', { name: 'Opt out or delete your data' })).toHaveAttribute('href', '/settings')
  expect(screen.getAllByRole('main')).toHaveLength(1)
})

test('home navigation anchors point at sections that exist', () => {
  renderHome()
  const nav = within(screen.getByRole('navigation', { name: 'Main' }))
  expect(nav.getByRole('link', { name: 'Ways to launch' })).toHaveAttribute('href', '#ways')
  expect(nav.getByRole('link', { name: 'How it works' })).toHaveAttribute('href', '#how')
  expect(nav.getByRole('link', { name: 'For captains' })).toHaveAttribute('href', '#captain')
  expect(nav.getByRole('link', { name: 'Privacy' })).toHaveAttribute('href', '#privacy')
  for (const id of ['ways', 'how', 'captain', 'privacy']) expect(document.getElementById(id)).not.toBeNull()
})

test('home keeps the scheme switch', () => {
  renderHome()
  expect(screen.getByRole('group')).toBeInTheDocument()
})

test('home names all three ways to launch and the captain tools', () => {
  renderHome()
  const ways = within(document.getElementById('ways')!)
  for (const tag of ['GitHub release', 'Meetup or talk', 'Manual launch']) expect(ways.getByText(tag)).toBeInTheDocument()
  const captain = within(document.getElementById('captain')!)
  for (const h of ['Your look on every card', 'Templates', 'See every post land']) expect(captain.getByRole('heading', { name: h })).toBeInTheDocument()
})
