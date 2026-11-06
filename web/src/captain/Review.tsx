import { useState } from 'react'
import Button from '../components/Button'
import { serviceName } from '../crew/format'
import type { CaptainDraft, Detail, Pr } from '../api'
import { asError, limitOf } from './format'

type Props = {
  detail: Detail
  pending: boolean
  onHidden: (hidden: number[]) => void
  onEditDraft: (draftId: string, text: string) => Promise<void>
  onSkipDraft: (draftId: string, skipped: boolean) => Promise<void>
  // Re-reads the captain's Buffer channels and reloads the launch; resolves to the channel count.
  onRefreshChannels: () => Promise<number>
}

// One of the captain's posts, with an editor that opens in place while the launch is still a draft.
function Post({
  d,
  canEdit,
  onEdit,
  onSkip,
}: {
  d: CaptainDraft
  canEdit: boolean
  onEdit: (id: string, text: string) => Promise<void>
  onSkip: (id: string, skipped: boolean) => Promise<void>
}) {
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState(d.text)
  const [saving, setSaving] = useState(false)
  const [problem, setProblem] = useState<string>()
  const [skipping, setSkipping] = useState(false)
  const limit = limitOf(d.service)
  const trimmed = text.trim()
  const over = limit !== undefined && [...trimmed].length > limit
  const unsent = d.status === 'draft' && !d.bufferPostId

  async function save() {
    setSaving(true)
    setProblem(undefined)
    try {
      await onEdit(d.id, trimmed)
      setEditing(false)
    } catch (e) {
      setProblem(asError(e).message)
    }
    setSaving(false)
  }

  async function skip(skipped: boolean) {
    setSkipping(true)
    setProblem(undefined)
    try {
      await onSkip(d.id, skipped)
    } catch (e) {
      setProblem(asError(e).message)
    }
    setSkipping(false)
  }

  if (d.status === 'skipped') {
    return (
      <li className="cap-post cap-post-skipped">
        <div className="cap-post-head">
          <span className="cap-muted">
            Not posting to {d.channelName} · {serviceName(d.service)}
          </span>
          {canEdit && (
            <Button variant="secondary" disabled={skipping} aria-label={`Add back ${d.channelName}`} onClick={() => skip(false)}>
              Add back
            </Button>
          )}
        </div>
        {problem && <p className="cap-err" role="alert">{problem}</p>}
      </li>
    )
  }

  return (
    <li className="cap-post">
      <div className="cap-post-head">
        <strong>{d.channelName}</strong>
        <span className={d.status === 'failed' ? 'cap-muted cap-err' : 'cap-muted'}>
          {serviceName(d.service)} · {d.status === 'draft' && d.bufferPostId ? 'saved as draft' : d.status === 'posted' ? 'Posted' : d.status}
        </span>
      </div>
      {d.note && <p className="cap-muted cap-post-note">{d.note}</p>}
      {editing ? (
        <div className="cap-edit">
          <label className="cap-field">
            <span className="visually-hidden">Edit post for {d.channelName}</span>
            <textarea className="field" rows={5} value={text} onChange={(e) => setText(e.target.value)} />
          </label>
          <div className="cap-edit-bar">
            <span className={over ? 'cap-count-line cap-err' : 'cap-count-line cap-muted'}>
              {[...trimmed].length}
              {limit ? ` / ${limit}` : ''}
            </span>
            <span className="cap-edit-actions">
              <Button variant="primary" disabled={saving || trimmed === '' || over || trimmed === d.text} onClick={save}>
                Save post
              </Button>
              <Button
                variant="secondary"
                disabled={saving}
                onClick={() => {
                  setText(d.text)
                  setProblem(undefined)
                  setEditing(false)
                }}
              >
                Cancel
              </Button>
            </span>
          </div>
          {problem && <p className="cap-err" role="alert">{problem}</p>}
        </div>
      ) : (
        <>
          <p className="cap-post-text">{d.text}</p>
          <div className="cap-post-foot">
            {limit && (
              <span className="cap-count-line cap-muted">
                {d.text.length} / {limit}
              </span>
            )}
            {canEdit && unsent && (
              <span className="cap-edit-actions">
                <Button
                  variant="secondary"
                  aria-label={`Edit draft for ${d.channelName}`}
                  onClick={() => {
                    setText(d.text)
                    setEditing(true)
                  }}
                >
                  Edit draft
                </Button>
                <Button variant="secondary" disabled={skipping} aria-label={`Remove post for ${d.channelName}`} onClick={() => skip(true)}>
                  Remove post
                </Button>
              </span>
            )}
          </div>
          {problem && <p className="cap-err" role="alert">{problem}</p>}
        </>
      )}
    </li>
  )
}

function PrRow({ pr, editable, pending, toggle }: { pr: Pr; editable: boolean; pending: boolean; toggle: () => void }) {
  return (
    <li className="cap-pr">
      <span>
        <span className="cap-mono">#{pr.prNumber}</span> {pr.title}
        <span className="cap-muted"> · {pr.authorLogin}</span>
      </span>
      {editable && (
        <Button variant="secondary" aria-pressed={pr.hidden} disabled={pending} onClick={toggle}>
          {pr.hidden ? 'Keep' : 'Hide'}
          <span className="visually-hidden"> #{pr.prNumber}</span>
        </Button>
      )}
    </li>
  )
}

// Left column: headline changes, the captain's own posts, hidden chores.
export default function Review({ detail, pending, onHidden, onEditDraft, onSkipDraft, onRefreshChannels }: Props) {
  const editable = detail.status === 'draft'
  const [checking, setChecking] = useState(false)
  const [checked, setChecked] = useState('')
  const refresh = async () => {
    setChecking(true)
    setChecked('')
    try {
      const n = await onRefreshChannels()
      setChecked(`Checked Buffer: ${n} ${n === 1 ? 'channel' : 'channels'}.`)
    } catch {
      setChecked('Could not reach Buffer, try again.')
    } finally {
      setChecking(false)
    }
  }
  const all = [...detail.headlines, ...detail.chores]
  const toggle = (n: number) => {
    const hidden = new Set(all.filter((p) => p.hidden).map((p) => p.prNumber))
    if (!hidden.delete(n)) hidden.add(n)
    onHidden([...hidden])
  }
  const shown = detail.headlines.filter((p) => !p.hidden)
  const hiddenPrs = all.filter((p) => p.hidden)
  const posts = detail.captainDrafts

  return (
    <div className="cap-col">
      {detail.source === 'github' && (
        <section className="cap-section panel" aria-labelledby="cap-headlines">
          <h2 id="cap-headlines">Headline changes</h2>
          {shown.length === 0 ? (
            <p className="cap-muted">No headline changes left. Keep one from the hidden list.</p>
          ) : (
            <ul className="cap-list">
              {shown.map((p) => (
                <PrRow key={p.prNumber} pr={p} editable={editable} pending={pending} toggle={() => toggle(p.prNumber)} />
              ))}
            </ul>
          )}
        </section>
      )}
      <section className="cap-section panel" aria-labelledby="cap-posts">
        <h2 id="cap-posts">Your posts</h2>
        {editable && (posts.length > 0 || detail.bufferConnected) && (
          <div className="cap-posts-head">
            {posts.length > 0 && (
              <p className="cap-muted cap-hint">Posts go to each channel you keep. Remove one to skip that channel.</p>
            )}
            {detail.bufferConnected && (
              <Button variant="secondary" disabled={checking} busy={checking} onClick={refresh}>
                {checking ? 'Checking Buffer...' : 'Refresh channels'}
              </Button>
            )}
          </div>
        )}
        <p className="cap-muted cap-hint" role="status">
          {checked}
        </p>
        {posts.length === 0 ? (
          <p className="cap-muted">
            {detail.bufferConnected ? 'Building your posts...' : 'Connect Buffer to see your posts here.'}
          </p>
        ) : (
          <ul className="cap-list">
            {posts.map((d) => (
              <Post key={d.id} d={d} canEdit={editable} onEdit={onEditDraft} onSkip={onSkipDraft} />
            ))}
          </ul>
        )}
      </section>
      {hiddenPrs.length > 0 && (
        <details className="cap-chores panel">
          <summary>
            {hiddenPrs.length} hidden {hiddenPrs.length === 1 ? 'chore' : 'chores'}
          </summary>
          <ul className="cap-list">
            {hiddenPrs.map((p) => (
              <PrRow key={p.prNumber} pr={p} editable={editable} pending={pending} toggle={() => toggle(p.prNumber)} />
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}
