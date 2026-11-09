import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { expect, test } from 'vitest'
import Select from './Select'

const OPTIONS = [
  { value: '', label: 'Built-in' },
  { value: 'a', label: 'Meetups' },
  { value: 'b', label: 'Releases' },
]

function Harness() {
  const [v, setV] = useState('')
  return (
    <>
      <Select aria-label="Template" options={OPTIONS} value={v} onChange={setV} />
      <output>{v || 'none'}</output>
    </>
  )
}

test('opens a themed list, marks the current pick and closes on choosing', async () => {
  render(<Harness />)
  const box = screen.getByRole('combobox', { name: 'Template' })
  expect(box).toHaveAttribute('aria-expanded', 'false')
  await userEvent.click(box)
  expect(box).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByRole('option', { name: 'Built-in' })).toHaveAttribute('aria-selected', 'true')
  await userEvent.click(screen.getByRole('option', { name: 'Releases' }))
  expect(screen.getByText('b')).toBeInTheDocument()
  expect(box).toHaveTextContent('Releases')
  expect(box).toHaveAttribute('aria-expanded', 'false')
})

test('works from the keyboard: arrows move, Enter picks, Escape closes, letters jump', async () => {
  render(<Harness />)
  const box = screen.getByRole('combobox', { name: 'Template' })
  box.focus()
  await userEvent.keyboard('{ArrowDown}')
  expect(box).toHaveAttribute('aria-expanded', 'true')
  await userEvent.keyboard('{ArrowDown}{Enter}')
  expect(box).toHaveTextContent('Meetups')
  await userEvent.keyboard('{Enter}{Escape}')
  expect(box).toHaveAttribute('aria-expanded', 'false')
  expect(box).toHaveTextContent('Meetups')
  await userEvent.keyboard('r')
  expect(box).toHaveTextContent('Releases')
  expect(box).toHaveFocus()
})

test('a click outside closes the list without changing the pick', async () => {
  render(<Harness />)
  await userEvent.click(screen.getByRole('combobox', { name: 'Template' }))
  await userEvent.click(document.body)
  expect(screen.queryByRole('listbox')).toBeNull()
  expect(screen.getByText('none')).toBeInTheDocument()
})
