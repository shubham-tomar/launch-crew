import Switch from './Switch'
import './components.css'

/** How a post goes out: a story (Instagram, Facebook), a swipeable PDF (LinkedIn), or '' for a plain post. */
export type Format = '' | 'story' | 'document'

/** The formats a channel on this service may choose, beyond a plain post. Mirrors buffer.FormatAllowed. */
export function formatsFor(service: string): Format[] {
  const s = service.toLowerCase()
  if (s === 'instagram' || s === 'facebook') return ['story']
  if (s === 'linkedin') return ['document']
  return []
}

export const STORY_HINT = "Stories show your images, not your text or link."
export const DOCUMENT_HINT = 'LinkedIn shows the card and images as pages to swipe through.'

type Props = {
  /** The channel's name, for the accessible labels ("Rocket: post type"). */
  channel: string
  formats: Format[]
  value: Format
  disabled?: boolean
  onChange: (v: Format) => void
}

/** A channel's post format: Post or Story for Instagram and Facebook, a Swipeable switch for LinkedIn. Nothing for other channels. */
export default function PostFormat({ channel, formats, value, disabled, onChange }: Props) {
  if (formats.includes('story')) {
    return (
      <div className="post-format">
        <div className="post-format-pills" role="group" aria-label={`${channel}: post type`}>
          {(['', 'story'] as const).map((f) => (
            <button
              key={f || 'post'}
              type="button"
              className="post-format-pill"
              aria-pressed={value === f}
              disabled={disabled}
              onClick={() => value !== f && onChange(f)}
            >
              {f === 'story' ? 'Story' : 'Post'}
            </button>
          ))}
        </div>
        {value === 'story' && <p className="post-format-hint">{STORY_HINT}</p>}
      </div>
    )
  }
  if (formats.includes('document')) {
    return (
      <div className="post-format">
        <Switch label="Swipeable (sent as a PDF)" name={channel} checked={value === 'document'} disabled={disabled} onChange={(on) => onChange(on ? 'document' : '')} />
        {value === 'document' && <p className="post-format-hint">{DOCUMENT_HINT}</p>}
      </div>
    )
  }
  return null
}
