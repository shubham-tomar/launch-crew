import { useState } from 'react'
import { useParams } from 'react-router'
import { pressLaunch, refreshChannels, retryComment, retryJob, type Detail, type Patch } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import Loader from '../components/Loader'
import Plane from '../components/Plane'
import Crew from './Crew'
import Frame from './Frame'
import { asError, moment, plusMinutes, stamp, stateTone, until, zone } from './format'
import Preflight from './Preflight'
import Review from './Review'
import Timing from './Timing'
import { useLaunch } from './useLaunch'

type Step = { name: string; note: string; state: 'done' | 'now' | 'next' }

// Where the launch is: release in, drafts built, crew joining, then the launch itself.
function Steps({ detail, launched }: { detail: Detail; launched: boolean }) {
  const mine = detail.captainDrafts.filter((d) => d.status !== 'skipped')
  const built = mine.length > 0
  const crewing = detail.crew.length > 0
  const raw = [
    { name: detail.source === 'github' ? 'Release detected' : 'Launch created', note: detail.releaseTag || 'by hand', done: true },
    { name: 'Drafts built', note: built ? `${mine.length} of yours` : 'building', done: built || launched },
    { name: 'Crew joining', note: `${detail.counts.in} of ${detail.counts.total} in`, done: launched },
    { name: 'Launch', note: detail.launchAt ? stamp(detail.launchAt) : 'time not set', done: launched },
  ]
  const now = raw.findIndex((s) => !s.done)
  const steps: Step[] = raw.map((s, i) => ({ name: s.name, note: s.note, state: s.done ? 'done' : i === now ? 'now' : 'next' }))
  if (!crewing && now === 2) steps[2].note = 'no one yet'
  const last = steps.length - 1
  return (
    <ol className="cap-flight" aria-label="Progress">
      {steps.map((s, i) => (
        <li key={s.name} className={`cap-leg cap-leg-${s.state}`} aria-current={s.state === 'now' ? 'step' : undefined}>
          <span className="cap-leg-air" aria-hidden="true">
            {s.state === 'now' && (
              <>
                <span className="cap-trail cap-trail-far" />
                <span className="cap-trail cap-trail-near" />
                <Plane deg={90} className="cap-leg-plane" />
              </>
            )}
          </span>
          <span className="cap-leg-track" aria-hidden="true">
            <span className={i === 0 ? 'cap-line cap-line-none' : s.state === 'next' ? 'cap-line cap-line-dashed' : 'cap-line'} />
            <span className="cap-node">
              {s.state === 'done' && (
                <svg viewBox="0 0 11 11">
                  <path d="M1 5.5 L4 8.5 L10 2" />
                </svg>
              )}
              {s.state === 'now' && <span className="cap-node-dot" />}
            </span>
            <span className={i === last ? 'cap-line cap-line-none' : s.state === 'done' ? 'cap-line' : 'cap-line cap-line-dashed'} />
          </span>
          <span className="cap-leg-text">
            <strong>{s.name}</strong>
            <span className="cap-muted">{s.note}</span>
          </span>
        </li>
      ))}
    </ol>
  )
}

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

// The launch's pass: what it is, who is in, and when the window opens.
function Hero({ detail, launched }: { detail: Detail; launched: boolean }) {
  const closed = detail.status === 'closed'
  const crewing = detail.crew.length > 0
  const chip = closed ? 'Closed' : launched ? 'Posting' : crewing ? 'Crew approving' : 'Draft'
  const prs = detail.source === 'github' ? detail.headlines.filter((p) => !p.hidden).length : 0
  const approved = detail.crew.filter((c) => stateTone(c.state) === 'ok').length
  const drafting = detail.crew.filter((c) => c.state === 'drafting').length
  const invited = detail.crew.filter((c) => c.state === 'invited').length
  // The captain is always in their own crew, and their posts are drafted for them.
  const facts = [
    prs > 0 && plural(prs, 'PR'),
    `${detail.counts.total + 1} crew`,
    `${approved + 1} approved`,
    drafting > 0 && `${drafting} drafting`,
    invited > 0 && `${invited} invited`,
  ].filter(Boolean) as string[]

  let label = 'Window opens in'
  let big = ''
  if (closed) {
    label = 'Window'
    big = 'Posted'
  } else if (launched) {
    const close = detail.launchAt ? until(plusMinutes(detail.launchAt, detail.windowMinutes)) : ''
    label = close ? 'Window closes in' : 'Window'
    big = close || 'Posted'
  } else if (!detail.launchAt) {
    big = 'Pick a launch time'
  } else {
    big = until(detail.launchAt) || 'Pick a later time'
  }
  return (
    <section className="cap-hero" aria-label="Launch summary">
      <div className="cap-hero-main">
        <span className="cap-hero-chip">
          <span className="cap-hero-dot" aria-hidden="true" />
          {chip}
        </span>
        <h1 className="cap-title cap-hero-title">{detail.title}</h1>
        <ul className="cap-hero-facts">
          {facts.map((f) => (
            <li key={f}>{f}</li>
          ))}
        </ul>
      </div>
      <div className="perf perf-v cap-hero-perf" aria-hidden="true" />
      <div className="perf perf-h cap-hero-perf-h" aria-hidden="true" />
      <div className="cap-hero-side">
        <span className="cap-eyebrow">{label}</span>
        <span className={big.length > 10 ? 'cap-hero-big cap-hero-big-small' : 'cap-hero-big'}>{big}</span>
        {detail.launchAt && (
          <span className="cap-hero-when">
            {moment(detail.launchAt)} · {zone()}
          </span>
        )}
      </div>
    </section>
  )
}

export default function LaunchDetail() {
  const id = useParams().id ?? ''
  const { detail, error, reload, patch, editDraft, skipDraft } = useLaunch(id)
  const [pending, setPending] = useState(false)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string>()
  const [pressed, setPressed] = useState(false)
  const [launchError, setLaunchError] = useState<string>()

  async function run(fn: () => Promise<unknown>) {
    setNotice(undefined)
    setPending(true)
    try {
      await fn()
    } catch (e) {
      setNotice(asError(e).message)
    }
    setPending(false)
  }
  const save = (p: Patch) => run(() => patch(p))
  // Timing saves itself as the captain types, so it handles its own errors.
  const saveTiming = async (p: Patch) => {
    setNotice(undefined)
    await patch(p)
  }

  async function launch() {
    setBusy(true)
    setLaunchError(undefined)
    try {
      await pressLaunch(id)
      setPressed(true)
      await reload()
    } catch (e) {
      const ex = asError(e)
      if (ex.code === 'already_launched') await reload()
      else {
        setLaunchError(ex.message)
        if (ex.code === 'preflight_red') await reload()
      }
    }
    setBusy(false)
  }

  if (error && !detail) {
    return (
      <Frame>
        <h1>{error.status === 404 ? 'Launch not found' : 'Could not load this launch'}</h1>
        <p>{error.status === 404 ? 'It may belong to another captain.' : error.message}</p>
      </Frame>
    )
  }
  if (!detail) {
    return (
      <Frame>
        <div aria-busy="true">
          <Loader label="Loading this launch" />
        </div>
      </Frame>
    )
  }

  const launched = detail.status !== 'draft' || pressed
  const manual = detail.source === 'manual'
  return (
    <Frame>
      {detail.deadLetters.map((d) => (
        <div key={d.jobId} className="cap-dead">
          <Alert tone="error" role="alert" lead={`Step failed: ${d.kind}.`}>
            {d.lastError} <span className="cap-muted">({d.attempts} attempts)</span>{' '}
            <Button variant="secondary" disabled={pending} onClick={() => run(async () => { await retryJob(id, d.jobId); await reload() })}>
              Retry
            </Button>
          </Alert>
        </div>
      ))}
      <Hero detail={detail} launched={launched} />
      {launched && (
        <p className="cap-muted">
          Launched · {detail.counts.in} of {detail.counts.total} in · {detail.openCount} scanned
        </p>
      )}
      <Steps detail={detail} launched={launched} />
      {notice && <Alert tone="error">{notice}</Alert>}
      <div className="cap-grid">
        <div className="cap-col">
          <Review
            detail={detail}
            pending={pending}
            onHidden={(hiddenPrs) => save({ hiddenPrs })}
            onEditDraft={(draftId, text) => editDraft(draftId, text)}
            onSkipDraft={(draftId, skipped) => skipDraft(draftId, skipped)}
            onRefreshChannels={async () => {
              const r = await refreshChannels(id)
              await reload()
              return r.channels
            }}
          />
          <Crew
            detail={detail}
            pending={pending}
            onTicked={(tickedCrew) => save({ tickedCrew })}
            onRetryComment={(memberId) => run(async () => { await retryComment(id, memberId); await reload() })}
          />
        </div>
        <div className="cap-col">
          <Timing detail={detail} onSave={saveTiming} />
          <Preflight
            checks={detail.checks}
            launched={launched}
            busy={busy}
            error={launchError}
            onLaunch={launch}
          />
          {manual && detail.status === 'draft' && (
            <p className="cap-muted cap-hint">Link, slide and PNG appear after Launch.</p>
          )}
          {manual && detail.showUrl && (
            <section className="cap-section panel cap-share" aria-labelledby="cap-share">
              <h2 id="cap-share">Share at the event</h2>
              <div className="cap-share-row">
                <a className="btn btn-secondary" href={detail.showUrl}>
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <rect x="3" y="4" width="18" height="12" rx="2" />
                    <path d="M8 20h8M12 16v4" />
                  </svg>
                  Show at the event
                </a>
                <a className="btn btn-secondary" href={detail.pngUrl} download>
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <path d="M12 4v11M7.5 10.5 12 15l4.5-4.5M5 20h14" />
                  </svg>
                  Download PNG
                </a>
              </div>
            </section>
          )}
        </div>
      </div>
    </Frame>
  )
}
