import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { listLaunches, type ApiError, type LaunchSummary } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import { SkeletonPanel } from '../components/Skeleton'
import Frame from './Frame'
import { asError, initials, stamp, until } from './format'
import { useNow } from './useNow'
import './launches.css'

type Tone = 'draft' | 'joining' | 'approving' | 'live' | 'closed'
type View = { tone: Tone; label: string; value: string; sub: string }

const plus = (
  <svg viewBox="0 0 14 14" aria-hidden="true">
    <path d="M7 2v10M2 7h10" />
  </svg>
)

const DAY = 86_400_000

// "3 days ago", "today", "yesterday".
function ago(iso: string, now: number) {
  const d = Math.floor((now - new Date(iso).getTime()) / DAY)
  if (d <= 0) return 'today'
  return d === 1 ? 'yesterday' : `${d} days ago`
}

// What the left panel of a launch card says, from its status and data alone.
export function viewOf(l: LaunchSummary, now = Date.now()): View {
  const when = l.launchAt ? `${stamp(l.launchAt)} · ${l.windowMinutes} min` : 'No time set yet'
  if (l.status === 'closed') {
    return { tone: 'closed', label: 'Posted', value: 'Closed', sub: l.launchAt ? `${stamp(l.launchAt)} · ${ago(l.launchAt, now)}` : 'Done' }
  }
  if (l.status === 'launched') {
    const end = l.launchAt ? new Date(new Date(l.launchAt).getTime() + l.windowMinutes * 60_000).toISOString() : null
    const left = until(end, now)
    return { tone: 'live', label: 'Live', value: 'Posting', sub: left ? `${left} left` : 'Window closing' }
  }
  const tone: Tone = l.crewTotal > 0 ? (l.crewIn > 0 ? 'approving' : 'joining') : 'draft'
  const label = tone === 'approving' ? 'Approving' : tone === 'joining' ? 'Joining' : 'Draft'
  const left = until(l.launchAt, now)
  return { tone, label, value: left || (l.launchAt ? 'Overdue' : 'Not set'), sub: when }
}

// The captain is always in their own crew and their posts are drafted for them, so the
// count reads "2 of 2" for a captain and one contributor, as the launch page does.
export function progress(l: LaunchSummary) {
  if (l.crewTotal === 0) return 'Just you so far'
  const word = l.status === 'closed' ? 'posted' : l.status === 'launched' ? 'queued' : 'approved'
  return `${l.crewIn + 1} of ${l.crewTotal + 1} ${word}`
}

// "2 passes on the board. 1 posting now. Next boarding in 2h 14m."
function subtitle(items: LaunchSummary[], now: number) {
  const open = items.filter((l) => l.status !== 'closed')
  if (open.length === 0) return 'Every launch has gone out.'
  const live = open.filter((l) => l.status === 'launched').length
  const next = open
    .filter((l) => l.status === 'draft' && l.launchAt && new Date(l.launchAt).getTime() > now)
    .sort((a, b) => new Date(a.launchAt!).getTime() - new Date(b.launchAt!).getTime())[0]
  const parts = [`${open.length} ${open.length === 1 ? 'pass' : 'passes'} on the board.`]
  if (live > 0) parts.push(`${live} posting now.`)
  if (next) parts.push(`Next boarding in ${until(next.launchAt, now)}.`)
  return parts.join(' ')
}

function NewLaunchButton() {
  return (
    <Link className="btn btn-primary ln-new" to="/captain/new">
      {plus}
      New launch
    </Link>
  )
}

function Ghost() {
  return (
    <div className="cap-ghost" aria-hidden="true">
      <div className="cap-ghost-main">
        <span className="cap-ghost-bar" />
        <span className="cap-ghost-block" />
        <span className="cap-ghost-bar" />
      </div>
      <div className="perf perf-v cap-ghost-perf" />
      <div className="cap-ghost-side">
        <span className="cap-ghost-block" />
        <span className="cap-ghost-bar" />
      </div>
    </div>
  )
}

function LaunchCard({ l, now }: { l: LaunchSummary; now: number }) {
  const v = viewOf(l, now)
  const people = [initials(l.captainName || 'You'), ...l.crewInitials]
  const shown = people.slice(0, 5)
  const total = l.crewTotal + 1
  const extra = Math.max(0, total - shown.length)
  const pct = Math.round(((l.crewIn + 1) / total) * 100)
  return (
    <li className="ln-item">
      <Link className="ln-card" to={`/captain/launches/${l.id}`} data-tone={v.tone}>
        <span className="ln-status">
          <span className="ln-eyebrow">
            {v.tone === 'closed' ? (
              <svg className="ln-tick" viewBox="0 0 11 11" aria-hidden="true">
                <path d="M1 5.5 L4 8.5 L10 2" />
              </svg>
            ) : (
              <span className={v.tone === 'joining' || v.tone === 'draft' ? 'ln-dot ln-dot-ring' : 'ln-dot'} aria-hidden="true" />
            )}
            {v.label}
          </span>
          <span className="ln-value">{v.value}</span>
          <span className="ln-sub">{v.sub}</span>
        </span>
        <span className="perf perf-v ln-perf-v" aria-hidden="true" />
        <span className="perf perf-h ln-perf-h" aria-hidden="true" />
        <span className="ln-body">
          <span className="ln-main">
            <span className="ln-title">{l.title}</span>
            <span className="ln-chips">
              {l.repo && <span className="ln-chip">{l.repo}</span>}
              {l.prCount > 0 && <span className="ln-chip">{l.prCount} PRs</span>}
              {l.releaseTag && !l.repo && <span className="ln-chip">{l.releaseTag}</span>}
              <span className="ln-chip">{l.source === 'github' ? 'GitHub release' : 'Manual launch'}</span>
            </span>
          </span>
          <span className="ln-crew">
            <span className="ln-avatars" aria-hidden="true">
              {shown.map((s, i) => (
                <span key={i} className={i === 0 ? 'ln-avatar ln-avatar-captain' : 'ln-avatar'}>
                  {s}
                </span>
              ))}
              {extra > 0 && <span className="ln-avatar ln-avatar-more">+{extra}</span>}
            </span>
            <span className="ln-bar" aria-hidden="true">
              <span style={{ inlineSize: `${pct}%` }} />
            </span>
            <span className="ln-progress">{progress(l)}</span>
          </span>
          <span className="ln-go" aria-hidden="true">
            <svg viewBox="0 0 16 16">
              <path d="M6 3.5 L10.5 8 L6 12.5" />
            </svg>
          </span>
        </span>
      </Link>
    </li>
  )
}

export default function Launches() {
  const [items, setItems] = useState<LaunchSummary[]>()
  const [error, setError] = useState<ApiError>()
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let live = true
    listLaunches()
      .then((l) => live && setItems(l))
      .catch((e) => live && setError(asError(e)))
    return () => {
      live = false
    }
  }, [attempt])

  const retry = () => {
    setError(undefined)
    setAttempt((n) => n + 1)
  }

  const now = useNow()
  return (
    <Frame>
      {error ? (
        <>
          <h1 className="cap-title">Launches</h1>
          <Alert tone="error">
            {error.message} <Button variant="secondary" onClick={retry}>Retry</Button>
          </Alert>
        </>
      ) : !items ? (
        <>
          <h1 className="cap-title">Launches</h1>
          <div role="status" aria-busy="true" className="ln-list">
            <span className="visually-hidden">Loading your launches</span>
            <SkeletonPanel lines={2} />
            <SkeletonPanel lines={2} />
            <SkeletonPanel lines={2} />
          </div>
        </>
      ) : items.length === 0 ? (
        <>
          <section className="cap-empty panel">
            <div className="cap-empty-copy">
              <span className="eyebrow">Board is clear</span>
              <h1 className="ln-empty-title">No launches yet, and that is a lovely place to start.</h1>
              <p className="cap-lede">
                Point Launch Crew at a release or a meetup. Everyone who helped gets a pass with their own post on it, written in their own
                words, waiting for their tap. You see the whole crew on one page before anything goes out.
              </p>
              <NewLaunchButton />
            </div>
            <Ghost />
          </section>
          <p className="ln-note">Nothing posts until each person taps approve, and only into their own Buffer queue.</p>
        </>
      ) : (
        <>
          <div className="ln-head">
            <div className="ln-head-text">
              <h1 className="ln-title-main">Launches</h1>
              <p className="ln-lede">{subtitle(items, now)}</p>
            </div>
            <NewLaunchButton />
          </div>
          <ul className="ln-list">
            {items.map((l) => (
              <LaunchCard key={l.id} l={l} now={now} />
            ))}
          </ul>
          <p className="ln-note">Nothing posts until each person taps approve, and only into their own Buffer queue.</p>
        </>
      )}
    </Frame>
  )
}
