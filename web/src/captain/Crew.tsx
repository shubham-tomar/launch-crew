import { useEffect, useRef, useState, type RefObject } from 'react'
import Icon from '../components/Icon'
import IconButton from '../components/IconButton'
import ServiceIcon from '../components/ServiceIcon'
import { serviceName } from '../crew/format'
import type { CrewPost, CrewRow, Detail } from '../api'
import { copy, initials, stateTone } from './format'

type Props = {
  detail: Detail
  pending: boolean
  onTicked: (ids: string[]) => void
  onRetryComment: (memberId: string) => void
}

const pickable = (c: CrewRow) => c.state === 'suggested' || c.state === 'ticked'

// Copies the link; when the clipboard is blocked it selects the field with the link
// (the one passed in, or its own read-only one) and says "Select and copy" instead.
export function CopyLink({ url, label, field }: { url: string; label: string; field?: RefObject<HTMLInputElement | null> }) {
  const [state, setState] = useState<'idle' | 'copied' | 'manual'>('idle')
  const own = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (state !== 'manual') return
    const el = field?.current ?? own.current
    el?.focus()
    el?.select()
  }, [state, field])

  useEffect(() => {
    if (state !== 'copied') return
    const t = window.setTimeout(() => setState('idle'), 2500)
    return () => window.clearTimeout(t)
  }, [state])

  return (
    <>
      <IconButton
        icon={state === 'copied' ? 'check' : 'copy'}
        tone={state === 'copied' ? 'accent' : 'default'}
        label={state === 'copied' ? 'Copied' : state === 'manual' ? 'Select and copy' : label}
        onClick={async () => setState((await copy(url)) ? 'copied' : 'manual')}
      />
      {state === 'manual' && !field && (
        <input ref={own} className="field cap-mono-field" readOnly value={url} aria-label="Invite link" onFocus={(e) => e.currentTarget.select()} />
      )}
    </>
  )
}

const postWord = (p: CrewPost) =>
  p.status === 'posted' ? 'posted' : p.status === 'queued' ? 'queued' : p.status === 'draft' ? 'draft' : 'failed in Buffer'

// Where a crew member's posts are: a link to each one on its network once sent, else its state.
function CrewPosts({ posts, name }: { posts: CrewPost[]; name: string }) {
  if (posts.length === 0) return null
  return (
    <ul className="cap-crew-posts" aria-label={`Posts by ${name}`}>
      {posts.map((p, i) => (
        <li key={`${p.network}-${i}`}>
          {p.status === 'posted' && p.externalLink ? (
            <a className="cap-link" href={p.externalLink} target="_blank" rel="noopener noreferrer">
              <ServiceIcon service={p.network} />
              View on {serviceName(p.network)}
            </a>
          ) : (
            <span className={p.status === 'failed_in_buffer' ? 'cap-crew-post cap-err' : 'cap-crew-post cap-muted'}>
              <ServiceIcon service={p.network} />
              {serviceName(p.network)} · {postWord(p)}
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}

// Right column, first section: who is in, with states; add by link for manual launches.
export default function Crew({ detail, pending, onTicked, onRetryComment }: Props) {
  const draft = detail.status === 'draft'
  const toggle = (id: string) => {
    const ticked = new Set(detail.crew.filter((c) => c.state === 'ticked').map((c) => c.id))
    if (!ticked.delete(id)) ticked.add(id)
    onTicked([...ticked])
  }
  const total = detail.counts.total + 1
  const captain = detail.captainName || 'You'
  const openLink = useRef<HTMLInputElement>(null)
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
              <span className="cap-state cap-state-err">comment failed</span>
            ) : (
              <span className={`cap-state cap-state-${stateTone(c.state)}`}>{c.state.replaceAll('_', ' ')}</span>
            )}
            <CrewPosts posts={c.posts ?? []} name={c.name} />
            {c.cardUrl && (
              <details className="cap-card-fold cap-crew-card">
                <summary className="cap-card-summary">
                  <Icon name="eye" />
                  <span>Card</span>
                  <Icon name="chevron" className="cap-card-chevron" />
                </summary>
                <img className="cap-card-img" src={c.cardUrl} alt={`Contributor card for ${c.name}`} loading="lazy" />
              </details>
            )}
            {c.commentFailed && (
              <div className="cap-crew-fail">
                <p className="cap-muted">
                  GitHub refused the comment. Retry once the app can comment on pull requests, or send them the link yourself.
                </p>
                <span className="cap-crew-fail-actions">
                  {c.commentRetriesLeft > 0 ? (
                    <IconButton icon="refresh" label={`Retry comment for ${c.name}`} disabled={pending} onClick={() => onRetryComment(c.id)} />
                  ) : (
                    <span className="cap-muted">Retried the most times allowed.</span>
                  )}
                  {c.inviteUrl && <CopyLink url={c.inviteUrl} label="Copy link" />}
                </span>
                {c.commentRetriesLeft > 0 && (
                  <span className="cap-muted">
                    {c.commentRetriesLeft} {c.commentRetriesLeft === 1 ? 'retry' : 'retries'} left
                  </span>
                )}
              </div>
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
              ref={openLink}
              className="field cap-mono-field"
              readOnly
              value={detail.openUrl}
              onFocus={(e) => e.currentTarget.select()}
            />
            <CopyLink url={detail.openUrl} label="Copy link" field={openLink} />
          </div>
        </div>
      )}
    </section>
  )
}
