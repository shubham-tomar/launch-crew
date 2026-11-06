import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import type { LaunchSummary } from '../api'
import Launches, { viewOf } from './Launches'

const base = (over: Partial<LaunchSummary> = {}): LaunchSummary => ({
  id: 'l1', status: 'draft', source: 'github', title: 'Split-pane resize ships in v2.4.0', releaseTag: 'v2.4.0',
  launchAt: new Date(Date.now() + 2 * 3600_000 + 14 * 60_000).toISOString(), windowMinutes: 90, openCount: 0,
  createdAt: new Date().toISOString(), repo: 'shubham-tomar/launch-crew', prCount: 14, crewIn: 6, crewTotal: 9,
  crewInitials: ['MK', 'DO', 'LF', 'TI', 'PN'], captainName: 'shubham-tomar', ...over,
})

function show(list: LaunchSummary[]) {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify(list), { status: 200 }))))
  render(
    <MemoryRouter>
      <Launches />
    </MemoryRouter>,
  )
}

test('a launch is one link named by its title, with repo, PR and source chips', async () => {
  show([base()])
  const link = await screen.findByRole('link', { name: /Split-pane resize ships in v2\.4\.0/ })
  expect(link).toHaveAttribute('href', '/captain/launches/l1')
  expect(within(link).getByText('shubham-tomar/launch-crew')).toBeInTheDocument()
  expect(within(link).getByText('14 PRs')).toBeInTheDocument()
  expect(within(link).getByText('GitHub release')).toBeInTheDocument()
})

test('shows progress, the +N overflow and the countdown', async () => {
  show([base()])
  const link = await screen.findByRole('link', { name: /Split-pane/ })
  // six crew plus the captain, out of nine plus the captain
  expect(within(link).getByText('7 of 10 approved')).toBeInTheDocument()
  expect(within(link).getByText('ST')).toBeInTheDocument()
  expect(within(link).getByText('+5')).toBeInTheDocument()
  expect(within(link).getByText('Approving')).toBeInTheDocument()
  expect(within(link).getByText(/^2h 1\dm$/)).toBeInTheDocument()
})

test('there is exactly one New launch button, no repo or by-hand buttons', async () => {
  show([base()])
  await screen.findByRole('link', { name: /Split-pane/ })
  expect(screen.getAllByRole('link', { name: 'New launch' })).toHaveLength(1)
  expect(screen.queryByRole('link', { name: 'Connect a repo' })).toBeNull()
  expect(screen.queryByRole('link', { name: 'Start one by hand' })).toBeNull()
})

test('a manual launch with no crew says so and has no repo chip', async () => {
  show([base({ id: 'm1', source: 'manual', title: 'Go Delhi Meetup', repo: '', prCount: 0, crewIn: 0, crewTotal: 0, crewInitials: [], releaseTag: '' })])
  const link = await screen.findByRole('link', { name: /Go Delhi Meetup/ })
  expect(within(link).getByText('Just you so far')).toBeInTheDocument()
  expect(within(link).getByText('Manual launch')).toBeInTheDocument()
  expect(within(link).getByText('Draft')).toBeInTheDocument()
})

test('live and closed launches read queued and posted', async () => {
  show([
    base({ id: 'a', status: 'launched', title: 'Live one', launchAt: new Date(Date.now() - 10 * 60_000).toISOString(), crewIn: 9, crewTotal: 9 }),
    base({ id: 'b', status: 'closed', title: 'Done one', launchAt: new Date(Date.now() - 3 * 86_400_000).toISOString(), crewIn: 7, crewTotal: 7 }),
  ])
  expect(within(await screen.findByRole('link', { name: /Live one/ })).getByText('10 of 10 queued')).toBeInTheDocument()
  const closed = screen.getByRole('link', { name: /Done one/ })
  expect(within(closed).getByText('8 of 8 posted')).toBeInTheDocument()
  expect(within(closed).getByText(/3 days ago/)).toBeInTheDocument()
})

test('viewOf: draft with crew who have not approved is Joining', () => {
  expect(viewOf(base({ crewIn: 0, crewTotal: 4 })).label).toBe('Joining')
  expect(viewOf(base({ crewIn: 0, crewTotal: 0 })).label).toBe('Draft')
})

test('offline shows a plain message and Retry fetches again', async () => {
  const fetchMock = vi.fn()
    .mockRejectedValueOnce(new TypeError('Failed to fetch'))
    .mockResolvedValueOnce(new Response(JSON.stringify([base()]), { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  render(
    <MemoryRouter>
      <Launches />
    </MemoryRouter>,
  )
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not reach Launch Crew. Check your connection and try again.')
  expect(screen.queryByText(/Failed to fetch/)).toBeNull()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('link', { name: /Split-pane/ })).toBeInTheDocument()
  expect(screen.queryByRole('alert')).toBeNull()
  expect(fetchMock).toHaveBeenCalledTimes(2)
})

test('the countdown ticks every minute and turns Overdue once the moment passes', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  try {
    show([base({ launchAt: new Date(Date.now() + 90_000).toISOString() })])
    const link = await screen.findByRole('link', { name: /Split-pane/ })
    expect(within(link).getByText('2m')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(within(link).getByText('1m')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(within(link).getByText('Overdue')).toBeInTheDocument()
  } finally {
    vi.useRealTimers()
  }
})
