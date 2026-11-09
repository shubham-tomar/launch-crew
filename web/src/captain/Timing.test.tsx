import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Detail, Patch } from '../api'
import Timing from './Timing'

const detail = (over: Partial<Detail> = {}): Detail => ({
  id: 'l1', status: 'draft', source: 'github', title: 't', summary: '', releaseTag: 'v1.0.0', releaseUrl: '',
  launchAt: new Date(Date.now() + 3 * 3600_000).toISOString(), windowMinutes: 90, crewCap: 200, openCount: 0,
  openUrl: '', showUrl: '', pngUrl: '', roles: [], bufferConnected: true, headlines: [], chores: [],
  captainDrafts: [], crew: [], checks: [], deadLetters: [], counts: { in: 0, total: 0 },
  captainName: 'Shubham Tomar', qrLogo: 'plane', qrLogoUrl: '', images: [], includeCard: true, ...over,
})

afterEach(() => vi.useRealTimers())

test('saves a new window by itself once it is valid, with no save button', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  const onSave = vi.fn<(p: Patch) => Promise<void>>().mockResolvedValue()
  render(<Timing detail={detail()} onSave={onSave} />)
  expect(screen.queryByRole('button', { name: /save timing/i })).toBeNull()

  await user.click(screen.getByRole('button', { name: /change moment or window/i }))
  const win = screen.getByLabelText(/window \(minutes\)/i)
  await user.clear(win)
  await user.type(win, '120')
  expect(onSave).not.toHaveBeenCalled()
  await act(() => vi.advanceTimersByTimeAsync(800))
  expect(onSave).toHaveBeenCalledTimes(1)
  expect(onSave).toHaveBeenCalledWith({ windowMinutes: 120 })
  expect(await screen.findByText('Saved')).toBeInTheDocument()
})

test('does not save a time in the past and says why', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const onSave = vi.fn().mockResolvedValue(undefined)
  const { container } = render(<Timing detail={detail({ launchAt: null })} onSave={onSave} />)
  const input = container.querySelector('input[type="datetime-local"]') as HTMLInputElement
  const { fireEvent } = await import('@testing-library/react')
  fireEvent.change(input, { target: { value: '2020-01-01T10:00' } })
  await act(() => vi.advanceTimersByTimeAsync(900))
  expect(onSave).not.toHaveBeenCalled()
  expect(screen.getByText('Pick a time in the future.')).toBeInTheDocument()
})

test('a launched launch shows the summary but no way to change it', () => {
  render(<Timing detail={detail({ status: 'launched' })} onSave={vi.fn()} />)
  expect(screen.queryByRole('button', { name: /change moment or window/i })).toBeNull()
  expect(screen.getByText('90 minutes')).toBeInTheDocument()
})

test('shows one bar per crew member, filled for those who approved', () => {
  const { container } = render(<Timing detail={detail({ counts: { in: 2, total: 5 } })} onSave={vi.fn()} />)
  expect(container.querySelectorAll('.cap-bar')).toHaveLength(5)
  expect(container.querySelectorAll('.cap-bar[data-ready]')).toHaveLength(2)
  expect(screen.getByText('5 posts, 18 min apart')).toBeInTheDocument()
  expect(screen.getByText(/2 slots ready · 3 waiting on approval/)).toBeInTheDocument()
})

test('a problem is announced after a pause in typing or on blur, not on every keystroke', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  const onSave = vi.fn().mockResolvedValue(undefined)
  render(<Timing detail={detail()} onSave={onSave} />)
  await user.click(screen.getByRole('button', { name: /change moment or window/i }))
  const win = screen.getByLabelText(/window \(minutes\)/i)
  await user.clear(win)
  await user.type(win, '2')
  await act(() => vi.advanceTimersByTimeAsync(300))
  expect(screen.getByRole('status')).toHaveTextContent('')
  await act(() => vi.advanceTimersByTimeAsync(400))
  expect(screen.getByRole('status')).toHaveTextContent('The window is 5 to 720 minutes.')
  expect(win).toHaveAttribute('aria-invalid', 'true')

  await user.clear(win)
  await user.type(win, '3')
  expect(screen.getByRole('status')).toHaveTextContent('')
  await user.tab()
  expect(screen.getByRole('status')).toHaveTextContent('The window is 5 to 720 minutes.')
  expect(onSave).not.toHaveBeenCalled()
})
