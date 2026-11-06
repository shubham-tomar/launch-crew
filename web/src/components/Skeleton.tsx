import './components.css'

type Props = {
  lines?: number
  /** What is loading, for screen readers. The blocks themselves are hidden. */
  label?: string
}

// Tint-coloured blocks at the text's line height with a slow shimmer, for list-shaped
// content that is on its way. Under reduced motion the blocks sit still.
export default function Skeleton({ lines = 3, label }: Props) {
  return (
    <div className="skeleton" role={label ? 'status' : undefined} aria-busy={label ? true : undefined}>
      {label && <span className="visually-hidden">{label}</span>}
      <div aria-hidden="true" className="skeleton-lines">
        {Array.from({ length: lines }, (_, i) => (
          <div key={i} className="skeleton-line" style={{ inlineSize: i === lines - 1 && lines > 1 ? '62%' : undefined }} />
        ))}
      </div>
    </div>
  )
}

// A page-shaped placeholder: a block (a card or a panel) made of shimmering lines.
export function SkeletonPanel({ lines = 3, className }: { lines?: number; className?: string }) {
  return (
    <div aria-hidden="true" className={['skeleton-panel', className].filter(Boolean).join(' ')}>
      <div className="skeleton-line skeleton-title" />
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className="skeleton-line" style={{ inlineSize: `${92 - i * 14}%` }} />
      ))}
    </div>
  )
}
