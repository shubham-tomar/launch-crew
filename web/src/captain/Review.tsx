import { useState } from 'react'
import Button from '../components/Button'
import SafeImg from '../components/SafeImg'
import Icon from '../components/Icon'
import IconButton from '../components/IconButton'
import ServiceIcon from '../components/ServiceIcon'
import { serviceName } from '../crew/format'
import { bufferChannelUrl, type CaptainDraft, type Detail, type Pr } from '../api'
import PostFormat, { type Format } from '../components/PostFormat'
import { asError, limitOf } from './format'

type Props = {
  detail: Detail
  pending: boolean
  onHidden: (hidden: number[]) => void
  onEditDraft: (draftId: string, text: string) => Promise<void>
  onSkipDraft: (draftId: string, skipped: boolean) => Promise<void>
  onFormat: (draftId: string, format: Format) => Promise<void>
  // Re-reads the captain's Buffer channels and reloads the launch; resolves to the channel count.
  onRefreshChannels: () => Promise<number>
}

const statusWord = (d: CaptainDraft) =>
  d.status === 'draft' && d.bufferPostId ? 'saved as draft' : d.status === 'posted' ? 'Posted' : d.status

// Where the captain can see the post: in Buffer once it is there, and on the network once sent.
function PostLinks({ d }: { d: CaptainDraft }) {
  if (!d.bufferPostId) return null
  return (
    <span className="cap-post-links">
      {d.status === 'posted' && d.externalLink && (
        <a className="cap-link" href={d.externalLink} target="_blank" rel="noopener noreferrer">
          <ServiceIcon service={d.service} />
          View on {serviceName(d.service)}
        </a>
      )}
      <a className="cap-link" href={bufferChannelUrl(d.channelId, d.status)} target="_blank" rel="noopener noreferrer">
        <Icon name="external" />
        Open in Buffer
      </a>
    </span>
  )
}

// One of the captain's posts, with an editor that opens in place while the launch is still a draft.
function Post({
  d,
  canEdit,
  onEdit,
  onSkip,
  onFormat,
}: {
  d: CaptainDraft
  canEdit: boolean
  onEdit: (id: string, text: string) => Promise<void>
  onSkip: (id: string, skipped: boolean) => Promise<void>
  onFormat: (id: string, format: Format) => Promise<void>
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

  const [formatting, setFormatting] = useState(false)
  async function pickFormat(format: Format) {
    setFormatting(true)
    setProblem(undefined)
    try {
      await onFormat(d.id, format)
    } catch (e) {
      setProblem(asError(e).message)
    }
    setFormatting(false)
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
          {canEdit && <IconButton icon="plus" label={`Add back ${d.channelName}`} busy={skipping} onClick={() => skip(false)} />}
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
          {serviceName(d.service)} · {statusWord(d)}
        </span>
      </div>
      {d.note && <p className="cap-muted cap-post-note">{d.note}</p>}
      {(d.formats?.length ?? 0) > 0 && (canEdit && unsent ? (
        <PostFormat channel={d.channelName} formats={d.formats ?? []} value={d.format ?? ''} disabled={formatting} onChange={pickFormat} />
      ) : d.format ? (
        <p className="cap-muted cap-post-note">{d.format === 'story' ? 'Posted as a story.' : 'Sent as a swipeable PDF.'}</p>
      ) : null)}
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
            <span className="cap-post-meta">
              {limit && (
                <span className="cap-count-line cap-muted">
                  {d.text.length} / {limit}
                </span>
              )}
              <PostLinks d={d} />
            </span>
            {canEdit && unsent && (
              <span className="cap-edit-actions">
                <IconButton
                  icon="pencil"
                  label={`Edit draft for ${d.channelName}`}
                  onClick={() => {
                    setText(d.text)
                    setEditing(true)
                  }}
                />
                <IconButton icon="minus" label={`Remove post for ${d.channelName}`} busy={skipping} onClick={() => skip(true)} />
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
        <IconButton
          icon={pr.hidden ? 'eye' : 'eye-off'}
          label={`${pr.hidden ? 'Keep' : 'Hide'} #${pr.prNumber}`}
          aria-pressed={pr.hidden}
          disabled={pending}
          onClick={toggle}
        />
      )}
    </li>
  )
}

// Left column: headline changes, the captain's own posts, hidden chores.
export default function Review({ detail, pending, onHidden, onEditDraft, onSkipDraft, onFormat, onRefreshChannels }: Props) {
  const editable = detail.status === 'draft'
  const [checking, setChecking] = useState(false)
  const [checked, setChecked] = useState('')
  const [choresOpen, setChoresOpen] = useState(false)
  const refresh = async () => {
    setChecking(true)
    setChecked('Checking Buffer…')
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
        <div className="cap-section-head">
          <h2 id="cap-posts">Your posts</h2>
          {editable && detail.bufferConnected && <IconButton icon="refresh" label="Refresh channels" busy={checking} onClick={refresh} />}
        </div>
        {detail.cardUrl && (
          <details className="cap-card-fold">
            <summary className="cap-card-summary">
              <Icon name="eye" />
              <span>Release card</span>
              <Icon name="chevron" className="cap-card-chevron" />
            </summary>
            <SafeImg className="cap-card-img" goneClassName="cap-card-gone" src={detail.cardUrl} alt={`Release card for ${detail.title}`} loading="lazy" />
          </details>
        )}
        {editable && posts.length > 0 && (
          <p className="cap-muted cap-hint">Posts go to each channel you keep. Remove one to skip that channel.</p>
        )}
        <p className="cap-muted cap-hint cap-checked" role="status">
          {checked}
        </p>
        {posts.length === 0 ? (
          <p className="cap-muted">
            {detail.bufferConnected ? 'Building your posts...' : 'Connect Buffer to see your posts here.'}
          </p>
        ) : (
          <ul className="cap-list">
            {posts.map((d) => (
              <Post key={d.id} d={d} canEdit={editable} onEdit={onEditDraft} onSkip={onSkipDraft} onFormat={onFormat} />
            ))}
          </ul>
        )}
      </section>
      {hiddenPrs.length > 0 && (
        <details className="cap-chores panel" open={choresOpen} onToggle={(e) => setChoresOpen(e.currentTarget.open)}>
          <summary>
            <span className="cap-chores-title">
              <Icon name="eye-off" />
              {hiddenPrs.length} hidden {hiddenPrs.length === 1 ? 'chore' : 'chores'}
            </span>
            <span className="cap-chores-cue">
              {choresOpen ? 'Hide' : 'Show'}
              <Icon name="chevron" className="cap-chores-chevron" />
            </span>
          </summary>
          <ul className="cap-list cap-chores-list">
            {hiddenPrs.map((p) => (
              <PrRow key={p.prNumber} pr={p} editable={editable} pending={pending} toggle={() => toggle(p.prNumber)} />
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}
