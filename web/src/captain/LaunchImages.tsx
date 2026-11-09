import { useState, type ChangeEvent, type DragEvent } from 'react'
import { IMAGE_NOT_IMAGE, IMAGE_TOO_LARGE, IMAGE_TYPES, MAX_IMAGES, MAX_IMAGE_BYTES, type LaunchImage } from '../api'
import Alert from '../components/Alert'
import Icon from '../components/Icon'
import IconButton from '../components/IconButton'
import SafeImg from '../components/SafeImg'
import Switch from '../components/Switch'
import './launch-images.css'

/** An upload on its way: progress is a percentage, 0 to 100; error means it was refused and stays until dismissed. */
export type Pending = { name: string; progress: number; error?: string }

type Props = {
  images: LaunchImage[]
  includeCard: boolean
  /** The launch has a card (hero or release card) to attach; without one the switch has nothing to do. */
  hasCard: boolean
  /** The launch is out: show the grid, allow nothing. */
  locked: boolean
  pending: Pending[]
  onAdd: (files: File[]) => void
  onRemove: (id: string) => void
  /** Move the image to the zero-based position `to`. */
  onMove: (id: string, to: number) => void
  onIncludeCard: (v: boolean) => void
  /** Drop a failed pending upload by its index. */
  onDismiss?: (index: number) => void
}

export const LOCKED = 'Images are locked once the launch is out.'
export const SMALL_NETWORKS_MAX = 4

/** The networks that take the fewest images, and what they get. */
export function capWarning(count: number, includeCard: boolean) {
  if (count <= SMALL_NETWORKS_MAX - 1) return ''
  const n = includeCard ? SMALL_NETWORKS_MAX - 1 : SMALL_NETWORKS_MAX
  return `X, Bluesky, Mastodon and Threads show up to ${SMALL_NETWORKS_MAX} images per post, so posts there get the first ${n} images${includeCard ? ' after the card' : ''}.`
}

/** Splits picked files into the ones to upload and a line per refused file. */
export function vetFiles(files: File[], room: number): { accepted: File[]; problems: string[] } {
  const accepted: File[] = []
  const problems: string[] = []
  for (const f of files) {
    if (!IMAGE_TYPES.includes(f.type)) problems.push(`${f.name}: ${IMAGE_NOT_IMAGE}`)
    else if (f.size > MAX_IMAGE_BYTES) problems.push(`${f.name}: ${IMAGE_TOO_LARGE}`)
    else accepted.push(f)
  }
  if (accepted.length > room) {
    problems.push(room > 0 ? `Only the first ${room} fit; a launch has up to ${MAX_IMAGES} images.` : `A launch has up to ${MAX_IMAGES} images.`)
    accepted.length = Math.max(room, 0)
  }
  return { accepted, problems }
}

// One stored image: numbered, with remove and move controls. A picture that no longer
// loads (R2 drops uploads after 60 days) says so in its place.
function Tile({ img, index, count, locked, onRemove, onMove, drag }: {
  img: LaunchImage
  index: number
  count: number
  locked: boolean
  onRemove: (id: string) => void
  onMove: (id: string, to: number) => void
  drag: { onStart: (id: string) => void; onDrop: (index: number) => void; dragging: boolean }
}) {
  const n = index + 1
  return (
    <li
      className={`li-tile${drag.dragging ? ' li-tile-dragging' : ''}`}
      draggable={!locked}
      onDragStart={() => drag.onStart(img.id)}
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => {
        e.preventDefault()
        drag.onDrop(index)
      }}
    >
      <span className="li-num" aria-hidden="true">{n}</span>
      <SafeImg src={img.url} alt={`Image ${n}`} goneClassName="li-gone" />
      {!locked && (
        <span className="li-controls">
          <IconButton icon="chevron" className="li-ctl li-earlier" label={`Move image ${n} earlier`} disabled={index === 0} onClick={() => onMove(img.id, index - 1)} />
          <IconButton icon="chevron" className="li-ctl li-later" label={`Move image ${n} later`} disabled={index === count - 1} onClick={() => onMove(img.id, index + 1)} />
          <IconButton icon="close" className="li-ctl li-remove" label={`Remove image ${n}`} onClick={() => onRemove(img.id)} />
        </span>
      )}
    </li>
  )
}

export default function LaunchImages({ images, includeCard, hasCard, locked, pending, onAdd, onRemove, onMove, onIncludeCard, onDismiss }: Props) {
  const [problems, setProblems] = useState<string[]>([])
  const [dragging, setDragging] = useState<string>()
  const [over, setOver] = useState(false)
  const room = MAX_IMAGES - images.length - pending.length
  const warning = capWarning(images.length, includeCard)

  function take(files: FileList | File[] | null) {
    const list = Array.from(files ?? [])
    if (list.length === 0) return
    const { accepted, problems } = vetFiles(list, room)
    setProblems(problems)
    if (accepted.length) onAdd(accepted)
  }
  const pick = (e: ChangeEvent<HTMLInputElement>) => {
    take(e.target.files)
    e.target.value = ''
  }
  const drop = (e: DragEvent) => {
    e.preventDefault()
    setOver(false)
    if (!locked && room > 0) take(e.dataTransfer.files)
  }
  const drag = {
    dragging: false,
    onStart: (id: string) => setDragging(id),
    onDrop: (to: number) => {
      if (dragging && images[to]?.id !== dragging) onMove(dragging, to)
      setDragging(undefined)
    },
  }

  return (
    <div className="li">
      {hasCard && <Switch label="Attach the launch card" checked={includeCard} disabled={locked} onChange={onIncludeCard} />}
      {locked && <p className="cap-muted li-locked">{LOCKED}</p>}
      {warning && <Alert tone="warning">{warning}</Alert>}
      {problems.length > 0 && (
        <Alert tone="error">
          {problems.map((p) => (
            <span key={p} className="li-problem">{p}</span>
          ))}
        </Alert>
      )}
      <ul className="li-grid" aria-label="Launch images">
        {images.map((img, i) => (
          <Tile key={img.id} img={img} index={i} count={images.length} locked={locked} onRemove={onRemove} onMove={onMove} drag={{ ...drag, dragging: dragging === img.id }} />
        ))}
        {pending.map((p, i) => (
          <li key={`${p.name}-${i}`} className={p.error ? 'li-tile li-pending li-failed' : 'li-tile li-pending'}>
            <span className="li-pending-name">{p.name}</span>
            {p.error ? (
              <>
                <span className="li-pending-error">{p.error}</span>
                {onDismiss && (
                  <button type="button" className="btn btn-secondary li-dismiss" aria-label={`Remove ${p.name}`} onClick={() => onDismiss(i)}>
                    Remove
                  </button>
                )}
              </>
            ) : (
              <span
                className="li-bar"
                role="progressbar"
                aria-label={`Uploading ${p.name}`}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={Math.round(p.progress)}
              >
                <span className="li-bar-fill" style={{ inlineSize: `${Math.min(100, Math.max(4, Math.round(p.progress)))}%` }} />
              </span>
            )}
          </li>
        ))}
        {!locked && room > 0 && (
          <li
            className={over ? 'li-tile li-add li-add-over' : 'li-tile li-add'}
            onDragOver={(e) => {
              e.preventDefault()
              setOver(true)
            }}
            onDragLeave={() => setOver(false)}
            onDrop={drop}
          >
            <label className="li-add-label">
              <input type="file" accept={IMAGE_TYPES.join(',')} multiple aria-label="Add images" onChange={pick} />
              <Icon name="plus" />
              <span className="li-add-text">Add images</span>
              <span className="li-add-hint">Up to {MAX_IMAGES} · 10 MB each</span>
            </label>
          </li>
        )}
      </ul>
    </div>
  )
}
