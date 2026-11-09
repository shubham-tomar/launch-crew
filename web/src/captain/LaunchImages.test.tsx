import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { LaunchImage } from '../api'
import LaunchImages, { type Pending } from './LaunchImages'

const imgs = (n: number): LaunchImage[] => Array.from({ length: n }, (_, i) => ({ id: `i${i + 1}`, url: `https://cdn.example/uploads/${i + 1}.jpg` }))

function fileOf(name: string, type: string, size = 1024) {
  const f = new File([new Uint8Array(8)], name, { type })
  Object.defineProperty(f, 'size', { value: size })
  return f
}

function setup(over: Partial<Parameters<typeof LaunchImages>[0]> = {}) {
  const props = {
    images: imgs(2),
    includeCard: true,
    hasCard: true,
    locked: false,
    pending: [] as Pending[],
    onAdd: vi.fn(),
    onRemove: vi.fn(),
    onMove: vi.fn(),
    onIncludeCard: vi.fn(),
    onDismiss: vi.fn(),
    ...over,
  }
  const view = render(<LaunchImages {...props} />)
  return { ...props, rerender: (next: Partial<typeof props>) => view.rerender(<LaunchImages {...props} {...next} />) }
}

const input = () => screen.getByLabelText('Add images') as HTMLInputElement

test('the add tile offers up to 10 and disappears at 10', () => {
  const { rerender } = setup({ images: imgs(9) })
  expect(input()).toBeInTheDocument()
  expect(screen.getByText('Up to 10 · 10 MB each')).toBeInTheDocument()
  rerender({ images: imgs(10) })
  expect(screen.queryByLabelText('Add images')).toBeNull()
})

test('an oversize file is refused in the browser and never passed on', () => {
  const { onAdd } = setup()
  fireEvent.change(input(), { target: { files: [fileOf('huge.jpg', 'image/jpeg', 11 << 20)] } })
  expect(screen.getByText(/huge\.jpg: Each image can be up to 10 MB\./)).toBeInTheDocument()
  expect(onAdd).not.toHaveBeenCalled()
})

test('a file that is not a supported image is refused by name', () => {
  const onAdd = vi.fn()
  setup({ onAdd })
  fireEvent.change(input(), { target: { files: [fileOf('photo.heic', 'image/heic'), fileOf('ok.png', 'image/png')] } })
  expect(screen.getByText(/photo\.heic: Use a JPEG, PNG, GIF or WebP image\./)).toBeInTheDocument()
  expect(onAdd).toHaveBeenCalledTimes(1)
  expect(onAdd.mock.calls[0][0].map((f: File) => f.name)).toEqual(['ok.png'])
})

test('files past the tenth are dropped with one message', () => {
  const onAdd = vi.fn()
  setup({ images: imgs(8), onAdd })
  fireEvent.change(input(), { target: { files: ['a', 'b', 'c', 'd'].map((n) => fileOf(`${n}.png`, 'image/png')) } })
  expect(screen.getByText('Only the first 2 fit; a launch has up to 10 images.')).toBeInTheDocument()
  expect(onAdd.mock.calls[0][0].map((f: File) => f.name)).toEqual(['a.png', 'b.png'])
})

test('warns past three images, and the copy follows the card switch', () => {
  const { rerender } = setup({ images: imgs(3) })
  expect(screen.queryByRole('status')).toBeNull()
  rerender({ images: imgs(4) })
  expect(screen.getByRole('status')).toHaveTextContent(
    'X, Bluesky, Mastodon and Threads show up to 4 images per post, so posts there get the first 3 images after the card.',
  )
  rerender({ images: imgs(4), includeCard: false })
  expect(screen.getByRole('status')).toHaveTextContent('so posts there get the first 4 images.')
})

test('move buttons call onMove with the target index and stop at the ends', async () => {
  const { onMove } = setup({ images: imgs(3) })
  expect(screen.getByRole('button', { name: 'Move image 1 earlier' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Move image 3 later' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: 'Move image 2 later' }))
  expect(onMove).toHaveBeenLastCalledWith('i2', 2)
  await userEvent.click(screen.getByRole('button', { name: 'Move image 3 earlier' }))
  expect(onMove).toHaveBeenLastCalledWith('i3', 1)
})

test('remove and the card switch call back', async () => {
  const { onRemove, onIncludeCard } = setup()
  await userEvent.click(screen.getByRole('button', { name: 'Remove image 1' }))
  expect(onRemove).toHaveBeenCalledWith('i1')
  const sw = screen.getByRole('switch', { name: 'Attach the launch card' })
  expect(sw).toBeChecked()
  await userEvent.click(sw)
  expect(onIncludeCard).toHaveBeenCalledWith(false)
})

test('a broken image says it was removed', () => {
  setup({ images: imgs(1) })
  fireEvent.error(screen.getByRole('img', { name: 'Image 1' }))
  expect(screen.getByText('Removed after 60 days')).toBeInTheDocument()
  expect(screen.queryByRole('img', { name: 'Image 1' })).toBeNull()
})

test('locked shows the grid read-only', () => {
  setup({ locked: true })
  expect(screen.getByText('Images are locked once the launch is out.')).toBeInTheDocument()
  expect(screen.queryByRole('button')).toBeNull()
  expect(screen.queryByLabelText('Add images')).toBeNull()
  expect(screen.getByRole('switch', { name: 'Attach the launch card' })).toBeDisabled()
})

test('pending uploads show progress; a failed one shows its error and can be removed', async () => {
  const { onDismiss } = setup({
    pending: [
      { name: 'a.png', progress: 40 },
      { name: 'b.heic', progress: 0, error: 'Use a JPEG, PNG, GIF or WebP image.' },
    ],
  })
  expect(screen.getByRole('progressbar', { name: 'Uploading a.png' })).toHaveAttribute('aria-valuenow', '40')
  expect(screen.getByText('Use a JPEG, PNG, GIF or WebP image.')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Remove b.heic' }))
  expect(onDismiss).toHaveBeenCalledWith(1)
})

test('without a card there is no switch to show', () => {
  setup({ hasCard: false })
  expect(screen.queryByRole('switch')).toBeNull()
  expect(screen.getByRole('img', { name: 'Image 1' })).toBeInTheDocument()
})
