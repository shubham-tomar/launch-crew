import { useState } from 'react'
import Button from '../components/Button'
import type { CrewRow, Detail } from '../api'
import { copy, initials, stateTone } from './format'

type Props = { detail: Detail; pending: boolean; onTicked: (ids: string[]) => void }

const pickable = (c: CrewRow) => c.state === 'suggested' || c.state === 'ticked'

function CopyLink({ url, label }: { url: string; label: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      variant="secondary"
      onClick={async () => {
        await copy(url)
        setDone(true)
      }}
    >
      {done ? 'Copied' : label}
    </Button>
  )
}

// Right column, first section: who is in, with states; add by link for manual launches.
export default function Crew({ detail, pending, onTicked }: Props) {
  const draft = detail.status === 'draft'
  const toggle = (id: string) => {
    const ticked = new Set(detail.crew.filter((c) => c.state === 'ticked').map((c) => c.id))
    if (!ticked.delete(id)) ticked.add(id)
    onTicked([...ticked])
  }
  const total = detail.counts.total + 1
  const captain = detail.captainName || 'You'
  return (
    <section className="cap-section panel" aria-labelledby="cap-crew">
      <h2 id="cap-crew">The crew · {total}</h2>
      <p className="cap-muted">
        {detail.counts.in + 1} of {total} in
        {detail.openCount > 0 && ` · ${detail.openCount} scanned`}
      </p>
      <ul className="cap-list">
        <li className="cap-crew-row cap-crew-grid cap-crew-captain">
          <span className="cap-person">
            <span className="cap-avatar" aria-hidden="true">{initials(captain)}</span>
            <span className="cap-person-name">{captain}</span>
          </span>
          <span className="cap-muted cap-crew-role">Captain</span>
          <span className="cap-state cap-state-you">you</span>
        </li>
        {detail.crew.map((c) => (
          <li key={c.id} className="cap-crew-row cap-crew-grid">
            {draft && pickable(c) ? (
              <label className="cap-tick">
                <input type="checkbox" checked={c.state === 'ticked'} disabled={pending} onChange={() => toggle(c.id)} />
                {c.name}
              </label>
            ) : (
              <span className="cap-person">
                <span className="cap-avatar" aria-hidden="true">{initials(c.name)}</span>
                <span className="cap-person-name">{c.name}</span>
              </span>
            )}
            <span className="cap-muted cap-crew-role">{c.role}</span>
            {c.commentFailed ? (
              c.inviteUrl ? (
                <CopyLink url={c.inviteUrl} label="comment failed · copy link" />
              ) : (
                <span className="cap-state cap-state-err">comment failed</span>
              )
            ) : (
              <span className={`cap-state cap-state-${stateTone(c.state)}`}>{c.state.replaceAll('_', ' ')}</span>
            )}
          </li>
        ))}
      </ul>
      {detail.crew.length === 0 && (
        <p className="cap-muted">
          {detail.source === 'github' ? 'No contributors found in this release.' : 'No one else has joined yet.'}
        </p>
      )}
      {detail.openUrl && (
        <div className="cap-addlink">
          <label htmlFor="cap-openlink">Add by link</label>
          <div className="cap-addlink-row">
            <input
              id="cap-openlink"
              className="field cap-mono-field"
              readOnly
              value={detail.openUrl}
              onFocus={(e) => e.currentTarget.select()}
            />
            <CopyLink url={detail.openUrl} label="Copy link" />
          </div>
        </div>
      )}
    </section>
  )
}
