import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { createLaunch, listTemplates, patchLaunch, previewQr, uploadLaunchImage, type NewLaunch as Body, type QrLogo, type Role, type Template } from '../api'
import LaunchImages from './LaunchImages'
import Loader from '../components/Loader'
import type { FailedUpload } from './LaunchDetail'
import Alert from '../components/Alert'
import Button from '../components/Button'
import Pass from '../components/Pass'
import Select from '../components/Select'
import Frame from './Frame'
import { asError, fromLocalInput } from './format'
import { MAX_WINDOW, MIN_WINDOW, launchAtProblem, windowProblem } from './timing-rules'
import './newlaunch.css'

const DAY = (v: string) => (v ? new Date(v).toLocaleDateString('en-US', { day: 'numeric', month: 'short' }) : 'Date TBD')

const MAX_ROLES = 4
const MAX_NAME = 24

type Source = 'github' | 'meetup' | 'manual'

const SOURCES: { id: Source; label: string }[] = [
  { id: 'github', label: 'GitHub release' },
  { id: 'meetup', label: 'Meetup or talk' },
  { id: 'manual', label: 'Manual post' },
]

const MEETUP_ROLES: Role[] = [
  { name: 'Attendee', hint: 'You were in the room' },
  { name: 'Speaker', hint: 'You gave a talk' },
  { name: 'Organiser', hint: 'You ran it' },
]

const QR_OPTIONS: { id: QrLogo; label: string }[] = [
  { id: 'plane', label: 'Plane' },
  { id: 'none', label: 'None' },
  { id: 'custom', label: 'My own image' },
]

const HTTPS = /^https:\/\/\S+$/i
const PREVIEW_DELAY = 600

type Slide = { png?: string; error?: string; loading: boolean }

// Redraws the event slide a moment after the title or the QR mark stops changing.
function useSlidePreview(title: string, qr: QrLogo, qrUrl: string): Slide {
  const [state, setState] = useState<Slide>({ loading: false })
  const ready = qr !== 'custom' || HTTPS.test(qrUrl)
  useEffect(() => {
    if (!ready) return
    let live = true
    const t = window.setTimeout(() => {
      setState((s) => ({ ...s, loading: true, error: undefined }))
      previewQr({ title, qrLogo: qr, ...(qr === 'custom' ? { qrLogoUrl: qrUrl } : {}) })
        .then((r) => live && setState({ png: r.png, loading: false }))
        .catch((e) => live && setState((s) => ({ ...s, loading: false, error: asError(e).message })))
    }, PREVIEW_DELAY)
    return () => {
      live = false
      window.clearTimeout(t)
    }
  }, [title, qr, qrUrl, ready])
  return state
}

// The hero as the browser loads it, keyed on the URL so a new link clears an old failure.
function HeroPreview({ url }: { url: string }) {
  const [broken, setBroken] = useState(false)
  return (
    <figure className="nl-hero-preview">
      {!broken && <img src={url} alt="Hero image preview" onError={() => setBroken(true)} />}
      <figcaption className={broken ? 'nl-error' : 'nl-hint'}>
        {broken ? 'That link did not load as an image. Check it opens on its own.' : 'Preview. The cards fade it behind the text.'}
      </figcaption>
    </figure>
  )
}

type Queued = { id: string; file: File; url: string }

// A preview URL for a picked file; empty where the browser (or jsdom) has none.
const objectURL = (f: File) => (typeof URL.createObjectURL === 'function' ? URL.createObjectURL(f) : '')

const sameRoles = (a: Role[], b: Role[]) => a.length === b.length && a.every((r, i) => r.name === b[i].name && r.hint === b[i].hint)

const check = (
  <svg viewBox="0 0 15 15" aria-hidden="true">
    <path d="M2 7.5 L6 11 L13 3.5" />
  </svg>
)

function GithubExplainer() {
  return (
    <section className="nl-explain" aria-label="GitHub release launches">
      <h2>Launch Crew starts this on its own.</h2>
      <p>
        Publish a release in a repo you have added and a draft launch appears here, with the crew found from the merged PRs. You review it
        and press Launch.
      </p>
      <p className="nl-hint">Nothing posts until each person taps approve.</p>
      <div className="nl-actions">
        <Link className="btn btn-primary" to="/captain/repos">
          Add a repo
        </Link>
        <Link className="btn btn-secondary" to="/captain/repos">
          See your repos
        </Link>
      </div>
    </section>
  )
}

export default function NewLaunch() {
  const navigate = useNavigate()
  const [source, setSource] = useState<Source>('meetup')
  const [title, setTitle] = useState('')
  const [summary, setSummary] = useState('')
  const [at, setAt] = useState('')
  const [win, setWin] = useState('90')
  const [titleError, setTitleError] = useState<string>()
  const [atError, setAtError] = useState<string>()
  const [winError, setWinError] = useState<string>()
  const [roles, setRoles] = useState<Role[]>(MEETUP_ROLES)
  const [hero, setHero] = useState('')
  const [heroError, setHeroError] = useState<string>()
  const [qr, setQr] = useState<QrLogo>('plane')
  const [qrUrl, setQrUrl] = useState('')
  const [qrError, setQrError] = useState<string>()
  const [templates, setTemplates] = useState<Template[]>([])
  const [templateId, setTemplateId] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  // Images are held here and go up once the launch exists.
  const [queued, setQueued] = useState<Queued[]>([])
  const [includeCard, setIncludeCard] = useState(true)
  const [uploadNote, setUploadNote] = useState('')

  function addFiles(files: File[]) {
    setQueued((q) => [...q, ...files.map((file, i) => ({ id: `q${Date.now()}-${q.length + i}`, file, url: objectURL(file) }))])
  }
  function removeQueued(id: string) {
    setQueued((q) => {
      const it = q.find((x) => x.id === id)
      if (it?.url && typeof URL.revokeObjectURL === 'function') URL.revokeObjectURL(it.url)
      return q.filter((x) => x.id !== id)
    })
  }
  function moveQueued(id: string, to: number) {
    setQueued((q) => {
      const from = q.findIndex((x) => x.id === id)
      if (from < 0 || to < 0 || to >= q.length) return q
      const next = [...q]
      const [it] = next.splice(from, 1)
      next.splice(to, 0, it)
      return next
    })
  }

  const slide = useSlidePreview(title.trim(), qr, qrUrl.trim())

  // The template picker is a nicety: if the list does not load, the built-in one is used.
  useEffect(() => {
    let live = true
    listTemplates()
      .then((v) => live && setTemplates(v.templates ?? []))
      .catch(() => {})
    return () => {
      live = false
    }
  }, [])

  const setRole = (i: number, patch: Partial<Role>) =>
    setRoles((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))

  // Meetup starts from the three usual roles; a manual post starts bare. Edited roles are kept.
  function pick(next: Source) {
    setSource(next)
    if (next === 'meetup' && roles.length === 0) setRoles(MEETUP_ROLES)
    if (next === 'manual' && sameRoles(roles, MEETUP_ROLES)) setRoles([])
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    setHeroError(undefined)
    setQrError(undefined)
    // The form is noValidate so every message sits by its field with aria-invalid, instead of
    // the browser's bubble. Timing follows the same rules as the launch page's Timing card;
    // an empty window takes the server default.
    const badTitle = title.trim() ? undefined : 'Give the launch a title.'
    const badAt = launchAtProblem(at)
    const badWin = win.trim() === '' ? undefined : windowProblem(win)
    setTitleError(badTitle)
    setAtError(badAt)
    setWinError(badWin)
    if (badTitle || badAt || badWin) return
    const url = hero.trim()
    if (url && !HTTPS.test(url)) {
      setHeroError('Use an https:// image URL.')
      return
    }
    const logo = qrUrl.trim()
    if (qr === 'custom' && !HTTPS.test(logo)) {
      setQrError('Use an https:// image URL.')
      return
    }
    const body: Body = { title: title.trim() }
    if (summary.trim()) body.summary = summary.trim()
    if (at) body.launchAt = fromLocalInput(at)
    if (win.trim() !== '') body.windowMinutes = Number(win)
    const named = roles
      .filter((r) => r.name.trim())
      .slice(0, MAX_ROLES)
      .map((r) => ({ name: r.name.trim().slice(0, MAX_NAME), hint: r.hint.trim() }))
    if (named.length) body.roles = named
    if (url) body.heroUrl = url
    if (qr === 'none') body.qrLogo = 'none'
    if (qr === 'custom') {
      body.qrLogo = 'custom'
      body.qrLogoUrl = logo
    }
    if (templateId) body.templateId = templateId
    setBusy(true)
    try {
      const d = await createLaunch(body)
      // The launch exists now; whatever fails from here is named on its page, never lost.
      const failed: FailedUpload[] = []
      if (!includeCard) {
        try {
          await patchLaunch(d.id, { includeCard: false })
        } catch (err) {
          failed.push({ name: 'Release card switch', error: `Could not turn the card off: ${asError(err).message}` })
        }
      }
      for (const [i, q] of queued.entries()) {
        setUploadNote(`Uploading image ${i + 1} of ${queued.length}…`)
        try {
          await uploadLaunchImage(d.id, q.file)
        } catch (err) {
          failed.push({ name: q.file.name, error: asError(err).message })
        }
      }
      navigate(`/captain/launches/${d.id}`, failed.length ? { state: { failedUploads: failed } } : undefined)
    } catch (err) {
      const ex = asError(err)
      const refused = ['hero_refused', 'hero_not_image', 'logo_refused', 'logo_not_image'].includes(ex.code)
      // Which image was refused: a qr_ code is the QR mark; the hero codes mean the hero unless only the QR image was given.
      const forQr = ex.code.startsWith('qr_') || (refused && qr === 'custom' && logo !== '' && url === '')
      if (refused && forQr) setQrError(ex.message)
      else if (refused) setHeroError(ex.message)
      else setError(ex.message)
      setUploadNote('')
      setBusy(false)
    }
  }

  const role = roles.find((r) => r.name.trim())?.name.trim() || 'Contributor'
  const sample = title.trim()
    ? `${title.trim()}${summary.trim() ? `. ${summary.trim()}` : ''}`
    : 'Your launch title and summary appear here, in each person’s own words.'

  return (
    <Frame>
      <div className="nl-top">
        <Link className="nl-back" to="/captain">
          <svg viewBox="0 0 16 16" aria-hidden="true">
            <path d="M10 3.5 L5.5 8 L10 12.5" />
          </svg>
          Launches
        </Link>
        <h1 className="nl-title">New launch</h1>
      </div>
      <fieldset className="nl-source">
        <legend>Where does this come from?</legend>
        <div className="nl-pills">
          {SOURCES.map((s) => (
            <button key={s.id} type="button" className="nl-pill" aria-pressed={source === s.id} disabled={busy} onClick={() => pick(s.id)}>
              {source === s.id && check}
              {s.label}
            </button>
          ))}
        </div>
      </fieldset>
      {source === 'github' && <GithubExplainer />}
      {source !== 'github' && busy && (
        <div className="nl-busy panel">
          <Loader label={uploadNote || 'Creating your launch'} hint="Drawing your cards. This can take a few seconds." />
        </div>
      )}
      {source !== 'github' && (
        // Hidden, not removed, while it saves: a refusal brings the form back as it was.
        <div className="nl-grid" hidden={busy}>
          <form className="nl-form" onSubmit={submit} noValidate>
            <div className="nl-intro">
              <h2>Set the moment</h2>
              <p>Launch Crew writes one draft per person and holds every one of them until that person approves it.</p>
            </div>
            <div className="nl-field">
              <label htmlFor="nl-title">Title</label>
              <input
                id="nl-title"
                className="field"
                type="text"
                required
                maxLength={120}
                aria-invalid={titleError ? true : undefined}
                aria-describedby={titleError ? 'nl-title-error nl-title-hint' : 'nl-title-hint'}
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
              {titleError && <span id="nl-title-error" role="alert" className="nl-error">{titleError}</span>}
              <span id="nl-title-hint" className="nl-hint">The crew sees this on their invite.</span>
            </div>
            <label className="nl-field">
              Summary
              <textarea className="field" rows={3} value={summary} onChange={(e) => setSummary(e.target.value)} />
            </label>
            <div className="nl-row">
              <div className="nl-field nl-grow">
                <label htmlFor="nl-at">Launch time</label>
                <input
                  id="nl-at"
                  className="field"
                  type="datetime-local"
                  value={at}
                  aria-invalid={atError ? true : undefined}
                  aria-describedby={atError ? 'nl-at-error' : undefined}
                  onChange={(e) => setAt(e.target.value)}
                />
                {atError && <span id="nl-at-error" role="alert" className="nl-error">{atError}</span>}
              </div>
              <div className="nl-field nl-narrow">
                <label htmlFor="nl-window">Window (minutes)</label>
                <input
                  id="nl-window"
                  className="field"
                  type="number"
                  min={MIN_WINDOW}
                  max={MAX_WINDOW}
                  aria-invalid={winError ? true : undefined}
                  aria-describedby={winError ? 'nl-window-error nl-window-hint' : 'nl-window-hint'}
                  value={win}
                  onChange={(e) => setWin(e.target.value)}
                />
                {winError && <span id="nl-window-error" role="alert" className="nl-error">{winError}</span>}
                <span id="nl-window-hint" className="nl-hint">Spread, not a pile-on.</span>
              </div>
            </div>
            <fieldset className="nl-fieldset">
              <legend>Roles</legend>
              <p className="nl-hint">
                {source === 'meetup'
                  ? 'Each role changes how a draft opens. A speaker’s starts with what they talked about; an attendee’s starts with what they took away. Up to four.'
                  : 'Leave empty for Attendee, Speaker and Organiser. Up to four.'}
              </p>
              {roles.map((r, i) => (
                <div key={i} className="nl-role">
                  <label className="nl-field">
                    <span className="visually-hidden">Role {i + 1} name</span>
                    <input
                      className="field"
                      type="text"
                      placeholder="Name"
                      aria-label={`Role ${i + 1} name`}
                      maxLength={MAX_NAME}
                      value={r.name}
                      onChange={(e) => setRole(i, { name: e.target.value })}
                    />
                  </label>
                  <label className="nl-field">
                    <span className="visually-hidden">Role {i + 1} hint</span>
                    <input
                      className="field"
                      type="text"
                      placeholder="Hint, e.g. spoke about"
                      aria-label={`Role ${i + 1} hint`}
                      maxLength={60}
                      value={r.hint}
                      onChange={(e) => setRole(i, { hint: e.target.value })}
                    />
                  </label>
                  <Button variant="secondary" onClick={() => setRoles((rs) => rs.filter((_, j) => j !== i))}>
                    Remove<span className="visually-hidden"> role {i + 1}</span>
                  </Button>
                </div>
              ))}
              <Button
                variant="secondary"
                disabled={roles.length >= MAX_ROLES}
                onClick={() => setRoles((rs) => [...rs, { name: '', hint: '' }])}
              >
                Add a role
              </Button>
            </fieldset>
            <div className="nl-field">
              <label htmlFor="nl-hero">Hero image URL (optional)</label>
              <input
                id="nl-hero"
                className="field"
                type="text"
                inputMode="url"
                value={hero}
                aria-invalid={heroError ? true : undefined}
                aria-describedby={heroError ? 'cap-hero-error nl-hero-hint' : 'nl-hero-hint'}
                onChange={(e) => setHero(e.target.value)}
              />
              <span id="nl-hero-hint" className="nl-hint">Sits behind every contributor card. Leave it empty and the cards stay typographic.</span>
              {HTTPS.test(hero.trim()) && <HeroPreview key={hero.trim()} url={hero.trim()} />}
            </div>
            {heroError && <span id="cap-hero-error" role="alert" className="nl-error">{heroError}</span>}
            <fieldset className="nl-fieldset">
              <legend>Images</legend>
              <p className="nl-hint">Attached to every post, in this order. They go up once the launch is created.</p>
              <LaunchImages
                images={queued.map((q) => ({ id: q.id, url: q.url }))}
                includeCard={includeCard}
                hasCard
                locked={false}
                pending={[]}
                onAdd={addFiles}
                onRemove={removeQueued}
                onMove={moveQueued}
                onIncludeCard={setIncludeCard}
              />
            </fieldset>
            <fieldset className="nl-fieldset">
              <legend>QR centre mark</legend>
              <p className="nl-hint">The small mark in the middle of the QR code on the event slide and PNG.</p>
              <div className="nl-seg">
                {QR_OPTIONS.map((o) => (
                  <label key={o.id} className="nl-seg-opt">
                    <input type="radio" name="qr-logo" value={o.id} checked={qr === o.id} onChange={() => setQr(o.id)} />
                    {o.label}
                  </label>
                ))}
              </div>
              {qr === 'custom' && (
                <label className="nl-field">
                  QR mark image URL
                  <input
                    className="field"
                    type="text"
                    inputMode="url"
                    placeholder="https://"
                    value={qrUrl}
                    aria-invalid={qrError ? true : undefined}
                    aria-describedby={qrError ? 'cap-qr-error' : undefined}
                    onChange={(e) => setQrUrl(e.target.value)}
                  />
                </label>
              )}
              {qrError && <span id="cap-qr-error" role="alert" className="nl-error">{qrError}</span>}
            </fieldset>
            <div className="nl-field">
              <label htmlFor="nl-template">Template</label>
              <Select
                id="nl-template"
                value={templateId}
                onChange={setTemplateId}
                aria-describedby="nl-template-hint"
                options={[{ value: '', label: 'Built-in' }, ...templates.map((t) => ({ value: t.id, label: t.name }))]}
              />
              <span id="nl-template-hint" className="nl-hint">
                The notes the model reads while drafting the crew's posts. <Link to="/captain/templates">Manage templates</Link>
              </span>
            </div>
            {error && <Alert tone="error">{error}</Alert>}
            <div className="nl-submit">
              <Button variant="primary" type="submit" busy={busy} disabled={busy}>
                Create launch
              </Button>
            </div>
          </form>
          <aside className="nl-preview" aria-label="Live preview">
            <span className="nl-kicker">Sample post</span>
            <div className="nl-post">
              <div className="nl-post-who">
                <span className="nl-avatar" aria-hidden="true">
                  {role[0].toUpperCase()}
                </span>
                <span className="nl-post-name">
                  <strong>A crew member</strong>
                  <span>@handle · X</span>
                </span>
              </div>
              <p className="nl-post-text">{sample}</p>
              <div className="nl-post-foot">
                <span>{sample.length} / 280</span>
                <span>{at ? `Goes out ${DAY(at)}` : 'Time not set'}</span>
              </div>
            </div>
            <span className="nl-kicker">Event slide</span>
            <figure className="nl-slide" aria-busy={slide.loading || undefined}>
              {slide.png ? (
                <img
                  src={slide.png}
                  alt={`Sample event slide for ${title.trim() || 'your launch'} with a QR code`}
                  className={slide.loading ? 'nl-slide-stale' : undefined}
                />
              ) : (
                <span className="nl-slide-empty skeleton-line" aria-hidden="true" />
              )}
              <figcaption className={slide.error ? 'nl-error' : 'nl-hint'}>
                {slide.error ??
                  (qr === 'custom' && !HTTPS.test(qrUrl.trim())
                    ? 'Add an https:// image URL to see your mark.'
                    : 'Show this at the event. The QR opens the launch so people can join in.')}
              </figcaption>
            </figure>
            <span className="nl-kicker">Contributor card</span>
            <Pass
              eyebrow={title.trim() || 'Your launch'}
              name="A contributor"
              meta={`@handle · ${role}`}
              stub={{ label: 'Pass', value: '#1', date: DAY(at) }}
            />
            <p className="nl-hint">A sample. Each person gets their own, rebuilt when the drafts go out.</p>
          </aside>
        </div>
      )}
    </Frame>
  )
}
