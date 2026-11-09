import { useEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { createTemplate, deleteTemplate, listTemplates, saveTemplate, type ApiError, type Template, type TemplatesView } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import { SkeletonPanel } from '../components/Skeleton'
import Frame from './Frame'
import { asError } from './format'
import { renderComment, templateProblem } from './comment-template'
import './templates.css'

const SAVED_FOR = 4000

// What the editor shows: the read-only built-in, a new template, or one of the captain's.
type Pick = { kind: 'builtin' } | { kind: 'new' } | { kind: 'edit'; id: string }

// Chips that drop a placeholder where the cursor is, so nobody has to remember the names.
function Placeholders({ items, onPick }: { items: string[]; onPick: (p: string) => void }) {
  return (
    <div className="tp-chips" role="group" aria-label="Placeholders">
      {items.map((p) => (
        <button key={p} type="button" className="tp-chip" onClick={() => onPick(p)}>
          {p}
        </button>
      ))}
    </div>
  )
}

export default function Templates() {
  const [data, setData] = useState<TemplatesView>()
  const [error, setError] = useState<ApiError>()
  const [attempt, setAttempt] = useState(0)
  const [pick, setPick] = useState<Pick>({ kind: 'builtin' })
  const [name, setName] = useState('')
  const [comment, setComment] = useState('')
  const [notes, setNotes] = useState('')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [problem, setProblem] = useState<string>()
  const [confirming, setConfirming] = useState(false)
  const box = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    let live = true
    listTemplates()
      .then((v) => live && setData(v))
      .catch((e) => live && setError(asError(e)))
    return () => {
      live = false
    }
  }, [attempt])

  useEffect(() => {
    if (!saved) return
    const t = window.setTimeout(() => setSaved(false), SAVED_FOR)
    return () => window.clearTimeout(t)
  }, [saved])

  const current: Template | undefined = pick.kind === 'edit' ? data?.templates.find((t) => t.id === pick.id) : undefined

  // Load the picked template into the editor; a new one starts blank.
  function choose(next: Pick) {
    setPick(next)
    setProblem(undefined)
    setSaved(false)
    setConfirming(false)
    const t = next.kind === 'edit' ? data?.templates.find((x) => x.id === next.id) : undefined
    setName(t?.name ?? '')
    setComment(t?.commentTemplate ?? '')
    setNotes(t?.draftNotes ?? '')
  }

  const dirty =
    pick.kind === 'new' ||
    (current !== undefined && (name !== current.name || comment !== current.commentTemplate || notes !== current.draftNotes))
  const commentProblem = data ? templateProblem(comment, data.maxCommentLen) : ''
  const notesOver = data !== undefined && [...notes.trim()].length > data.maxNotesLen
  const nameProblem = name.trim() === '' ? 'Give the template a name.' : data && [...name.trim()].length > data.maxNameLen ? `Keep the name under ${data.maxNameLen} characters.` : ''
  const atLimit = data !== undefined && data.templates.length >= data.maxTemplates

  async function save() {
    if (!data || pick.kind === 'builtin') return
    setSaving(true)
    setProblem(undefined)
    setSaved(false)
    const input = { name: name.trim(), commentTemplate: comment, draftNotes: notes }
    try {
      const t = pick.kind === 'new' ? await createTemplate(input) : await saveTemplate(pick.id, input)
      const others = data.templates.filter((x) => x.id !== t.id)
      setData({ ...data, templates: [...others, t].sort((a, b) => a.name.localeCompare(b.name)) })
      setPick({ kind: 'edit', id: t.id })
      setName(t.name)
      setComment(t.commentTemplate)
      setNotes(t.draftNotes)
      setSaved(true)
    } catch (e) {
      setProblem(asError(e).message)
    }
    setSaving(false)
  }

  async function remove() {
    if (!data || pick.kind !== 'edit') return
    setSaving(true)
    setProblem(undefined)
    try {
      await deleteTemplate(pick.id)
      setData({ ...data, templates: data.templates.filter((t) => t.id !== pick.id) })
      choose({ kind: 'builtin' })
    } catch (e) {
      setProblem(asError(e).message)
    }
    setSaving(false)
  }

  // Insert at the cursor and put the caret after the placeholder.
  function insert(p: string) {
    const el = box.current
    if (!el) {
      setComment((c) => c + p)
      return
    }
    const start = el.selectionStart ?? comment.length
    const end = el.selectionEnd ?? start
    const next = comment.slice(0, start) + p + comment.slice(end)
    // Commit the value now, so the caret can be placed after the placeholder at once
    // instead of on a later tick that could pull focus from wherever the person went.
    flushSync(() => setComment(next))
    el.focus()
    el.setSelectionRange(start + p.length, start + p.length)
  }

  return (
    <Frame>
      <div className="tp-head">
        <h1 className="tp-title">Templates</h1>
        <p className="tp-lede">
          How Launch Crew speaks for you: the comment it leaves on each contributor's pull request, and notes the drafting model reads
          while writing the crew's posts. Keep several and pick one per repo or per launch. Anything without a template uses the built-in
          one.
        </p>
      </div>
      {error && !data && (
        <Alert tone="error">
          {error.message}{' '}
          <Button variant="secondary" onClick={() => { setError(undefined); setAttempt((n) => n + 1) }}>Retry</Button>
        </Alert>
      )}
      {!data && !error && (
        <div role="status" aria-busy="true" className="tp-grid">
          <span className="visually-hidden">Loading your templates</span>
          <SkeletonPanel lines={5} />
          <SkeletonPanel lines={3} />
        </div>
      )}
      {data && (
        <>
          <div className="tp-list" role="group" aria-label="Your templates">
            <button type="button" className="tp-tab" aria-pressed={pick.kind === 'builtin'} onClick={() => choose({ kind: 'builtin' })}>
              Built-in
            </button>
            {data.templates.map((t) => (
              <button
                key={t.id}
                type="button"
                className="tp-tab"
                aria-pressed={pick.kind === 'edit' && pick.id === t.id}
                onClick={() => choose({ kind: 'edit', id: t.id })}
              >
                {t.name}
              </button>
            ))}
            <button
              type="button"
              className="tp-tab tp-tab-new"
              aria-pressed={pick.kind === 'new'}
              disabled={atLimit}
              title={atLimit ? `You can keep up to ${data.maxTemplates} templates` : undefined}
              onClick={() => choose({ kind: 'new' })}
            >
              + New template
            </button>
          </div>

          {pick.kind === 'builtin' ? (
            <section className="panel tp-section" aria-labelledby="tp-builtin">
              <h2 id="tp-builtin">Built-in</h2>
              <p className="cap-muted">
                Used by every repo and launch that has no template picked. It can't be edited; make a new template to change the wording
                or add notes for the drafts.
              </p>
              <h3 className="tp-preview-title">PR comment</h3>
              <pre className="tp-preview" aria-label="Comment preview">
                {renderComment('', data.defaultComment)}
              </pre>
              {!atLimit && (
                <div>
                  <Button variant="secondary" onClick={() => choose({ kind: 'new' })}>
                    New template
                  </Button>
                </div>
              )}
            </section>
          ) : (
            <form
              className="tp-grid"
              noValidate
              aria-label={pick.kind === 'new' ? 'New template' : `Template ${current?.name ?? ''}`}
              onSubmit={(e) => {
                e.preventDefault()
                save()
              }}
            >
              <section className="panel tp-section" aria-labelledby="tp-comment">
                <label className="cap-field" htmlFor="tp-name">
                  Template name
                </label>
                <input
                  id="tp-name"
                  className="field"
                  type="text"
                  maxLength={data.maxNameLen + 10}
                  placeholder="Meetups, Big releases…"
                  value={name}
                  aria-invalid={name !== '' && nameProblem ? true : undefined}
                  onChange={(e) => setName(e.target.value)}
                />
                <h2 id="tp-comment">PR comment</h2>
                <p className="cap-muted">
                  Posted once on each credited pull request. The consent line and the opt-out link are always kept, even if you leave them
                  out.
                </p>
                <label className="cap-field" htmlFor="tp-comment-box">
                  Comment template
                </label>
                <textarea
                  id="tp-comment-box"
                  ref={box}
                  className="field tp-box"
                  rows={7}
                  value={comment}
                  placeholder={data.defaultComment}
                  aria-invalid={commentProblem ? true : undefined}
                  aria-describedby="tp-comment-help"
                  onChange={(e) => setComment(e.target.value)}
                />
                <div className="tp-bar">
                  <Placeholders items={data.placeholders} onPick={insert} />
                  <span className={[...comment].length > data.maxCommentLen ? 'cap-count-line cap-err' : 'cap-count-line cap-muted'}>
                    {[...comment].length} / {data.maxCommentLen}
                  </span>
                </div>
                <p id="tp-comment-help" className={commentProblem ? 'cap-err tp-help' : 'cap-muted tp-help'}>
                  {commentProblem || 'Leave it empty to use the built-in comment.'}
                </p>
                {comment.trim() !== '' && (
                  <Button variant="secondary" onClick={() => setComment('')}>
                    Use the built-in comment
                  </Button>
                )}
                <h3 className="tp-preview-title">Preview</h3>
                <pre className="tp-preview" aria-label="Comment preview">
                  {renderComment(comment, data.defaultComment)}
                </pre>
              </section>

              <section className="panel tp-section" aria-labelledby="tp-notes">
                <h2 id="tp-notes">Notes for the drafts</h2>
                <p className="cap-muted">
                  What the model should know when it drafts the crew's posts: a hashtag to use, a Discord to mention, a word to avoid. Each
                  person still edits their own post and nothing goes out without their tap.
                </p>
                <label className="cap-field" htmlFor="tp-notes-box">
                  Drafting notes
                </label>
                <textarea
                  id="tp-notes-box"
                  className="field tp-box"
                  rows={5}
                  value={notes}
                  placeholder="Mention our Discord at discord.gg/rocket. Call it rocket, never Rocket App."
                  aria-invalid={notesOver || undefined}
                  onChange={(e) => setNotes(e.target.value)}
                />
                <span className={notesOver ? 'cap-count-line cap-err' : 'cap-count-line cap-muted'}>
                  {[...notes.trim()].length} / {data.maxNotesLen}
                </span>
              </section>

              <div className="tp-actions">
                <Button
                  type="submit"
                  variant="primary"
                  disabled={!dirty || !!nameProblem || !!commentProblem || notesOver || saving}
                  busy={saving}
                >
                  {saving ? 'Saving…' : pick.kind === 'new' ? 'Create template' : 'Save template'}
                </Button>
                {pick.kind === 'edit' && !confirming && (
                  <Button variant="secondary" disabled={saving} onClick={() => setConfirming(true)}>
                    Delete
                  </Button>
                )}
                {confirming && (
                  <Alert tone="warning">
                    Delete “{current?.name}”? Repos and launches using it go back to the built-in template.{' '}
                    <Button variant="secondary" onClick={remove} busy={saving}>
                      Delete template
                    </Button>{' '}
                    <Button variant="secondary" onClick={() => setConfirming(false)}>
                      Keep it
                    </Button>
                  </Alert>
                )}
                {name !== '' && nameProblem && <span className="cap-err">{nameProblem}</span>}
                {saved && <Alert tone="success" lead="Saved.">Repos and launches using it pick it up on their next comment and drafts.</Alert>}
                {problem && <Alert tone="error">{problem}</Alert>}
              </div>
            </form>
          )}
        </>
      )}
    </Frame>
  )
}
