import { useEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { getSettings, saveSettings, type ApiError, type CaptainSettings } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import { SkeletonPanel } from '../components/Skeleton'
import Frame from './Frame'
import { asError } from './format'
import { renderComment, templateProblem } from './comment-template'
import './templates.css'

const SAVED_FOR = 4000

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
  const [data, setData] = useState<CaptainSettings>()
  const [error, setError] = useState<ApiError>()
  const [attempt, setAttempt] = useState(0)
  const [comment, setComment] = useState('')
  const [notes, setNotes] = useState('')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [problem, setProblem] = useState<string>()
  const box = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    let live = true
    getSettings()
      .then((s) => {
        if (!live) return
        setData(s)
        setComment(s.commentTemplate)
        setNotes(s.draftNotes)
      })
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

  const dirty = data !== undefined && (comment !== data.commentTemplate || notes !== data.draftNotes)
  const commentProblem = data ? templateProblem(comment, data.maxCommentLen) : ''
  const notesOver = data !== undefined && [...notes.trim()].length > data.maxNotesLen

  async function save() {
    if (!data) return
    setSaving(true)
    setProblem(undefined)
    setSaved(false)
    try {
      const s = await saveSettings({ commentTemplate: comment, draftNotes: notes })
      setData(s)
      setComment(s.commentTemplate)
      setNotes(s.draftNotes)
      setSaved(true)
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
          while writing the crew's posts. Both apply to every launch you run.
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
        <form
          className="tp-grid"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            save()
          }}
        >
          <section className="panel tp-section" aria-labelledby="tp-comment">
            <h2 id="tp-comment">PR comment</h2>
            <p className="cap-muted">
              Posted once on each credited pull request. The consent line and the opt-out link are always kept, even if you leave them out.
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
            <Button type="submit" variant="primary" disabled={!dirty || !!commentProblem || notesOver || saving} busy={saving}>
              {saving ? 'Saving…' : 'Save templates'}
            </Button>
            {saved && <Alert tone="success" lead="Saved.">Your next launch uses these.</Alert>}
            {problem && <Alert tone="error">{problem}</Alert>}
          </div>
        </form>
      )}
    </Frame>
  )
}
