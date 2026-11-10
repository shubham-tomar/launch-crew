import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import PostFormat, { DOCUMENT_HINT, STORY_HINT, formatsFor } from './PostFormat'

test('Instagram and Facebook choose Post or Story; LinkedIn gets Swipeable; others nothing', () => {
  expect(formatsFor('Instagram')).toEqual(['story'])
  expect(formatsFor('facebook')).toEqual(['story'])
  expect(formatsFor('linkedin')).toEqual(['document'])
  expect(formatsFor('twitter')).toEqual([])
  const { container } = render(<PostFormat channel="Ada" formats={[]} value="" onChange={() => {}} />)
  expect(container).toBeEmptyDOMElement()
})

test('picking Story says what a story leaves out', async () => {
  const onChange = vi.fn()
  const { rerender } = render(<PostFormat channel="Rocket" formats={['story']} value="" onChange={onChange} />)
  expect(screen.getByRole('group', { name: 'Rocket: post type' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Post' })).toHaveAttribute('aria-pressed', 'true')
  await userEvent.click(screen.getByRole('button', { name: 'Story' }))
  expect(onChange).toHaveBeenCalledWith('story')
  rerender(<PostFormat channel="Rocket" formats={['story']} value="story" onChange={onChange} />)
  expect(screen.getByText(STORY_HINT)).toBeInTheDocument()
  // Pressing the chosen one again changes nothing.
  await userEvent.click(screen.getByRole('button', { name: 'Story' }))
  expect(onChange).toHaveBeenCalledTimes(1)
})

test('the LinkedIn switch turns the PDF on and off', async () => {
  const onChange = vi.fn()
  const { rerender } = render(<PostFormat channel="Rocket" formats={['document']} value="" onChange={onChange} />)
  const sw = screen.getByRole('switch', { name: 'Rocket: swipeable (sent as a pdf)' })
  await userEvent.click(sw)
  expect(onChange).toHaveBeenLastCalledWith('document')
  rerender(<PostFormat channel="Rocket" formats={['document']} value="document" onChange={onChange} />)
  expect(screen.getByText(DOCUMENT_HINT)).toBeInTheDocument()
  await userEvent.click(screen.getByRole('switch', { name: 'Rocket: swipeable (sent as a pdf)' }))
  expect(onChange).toHaveBeenLastCalledWith('')
})
