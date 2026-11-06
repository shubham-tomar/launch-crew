import { useEffect, useRef, useState } from 'react'
import type { KeyboardEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { ApiError, connect, disconnect, post, refreshMyChannels, saveDraft, updateVoice, type ApproveView, type Outcome, type Timing } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import Chip from '../components/Chip'
import DraftBox from '../components/DraftBox'
import IconButton from '../components/IconButton'
import Loader from '../components/Loader'
import Pass from '../components/Pass'
import ServiceIcon from '../components/ServiceIcon'
import VoiceCard, { type VoiceEdit } from '../components/VoiceCard'
import Results from './Results'
import { serviceName } from './format'
import { CrewFrame, Dead, Foreign, useInvite } from './shared'
import { useDrafts } from './useDrafts'

const LIMITS: Record<string, number> = { x: 280, twitter: 280, threads: 500, bluesky: 300, mastodon: 500, linkedin: 3000 }
const TIMING: Record<Timing, string> = { window: 'Land in the window', queue: 'Next free slot', draft: 'Save as draft' }
const TIMING_HINT: Record<Timing, string> = {
  window: 'Goes out at a random moment inside the launch window.',
  queue: 'Goes to the next open slot in your Buffer queue.',
  draft: 'Saved in Buffer as a draft. Nothing is scheduled.',
}
const BUSY = 'Buffer is not answering, try again'
// "your edits are kept" is reassurance for a moment, not a fixture; it also goes on the next edit.
const KEPT_FOR = 4000

const passDate = (iso: string | null) => (iso ? new Date(iso).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' }) : '')

// One reconnect per 401: the flag survives the OAuth round trip, so coming back to another
// 401 shows a message instead of bouncing to Buffer again. Storage may be blocked; then we bounce.
const reconnectKey = (token: string) => `lc-reconnect:${token}`
const triedReconnect = (token: string) => {
  try {
    return sessionStorage.getItem(reconnectKey(token)) === '1'
  } catch {
    return false
  }
}
const setTriedReconnect = (token: string, on: boolean) => {
  try {
    if (on) sessionStorage.setItem(reconnectKey(token), '1')
    else sessionStorage.removeItem(reconnectKey(token))
  } catch {
    /* storage blocked: nothing to remember */
  }
}

function TrustStrip({ memberId, token }: { memberId?: string; token: string }) {
  const navigate = useNavigate()
  const [asking, setAsking] = useState(false)
  const [failed, setFailed] = useState(false)
  async function leave() {
    try {
      if (memberId) await disconnect(memberId, token)
    } catch {
      setFailed(true)
      setAsking(false)
      return
    }
    navigate(`/s/${encodeURIComponent(token)}`)
  }
  return (
    <>
    {failed && <Alert tone="error" role="alert">Couldn't disconnect, try again</Alert>}
    <p className="crew-caption crew-fine crew-trust">
      adds drafts to your queue only · nothing posts without your tap ·{' '}
      {asking ? (
        <span>
          Disconnect Buffer?{' '}
          <button type="button" className="crew-inline" onClick={leave}>Yes</button>{' '}
          <button type="button" className="crew-inline" onClick={() => setAsking(false)}>No</button>
        </span>
      ) : (
        <button type="button" className="crew-inline" onClick={() => setAsking(true)}>disconnect</button>
      )}{' '}
      any time
    </p>
    </>
  )
}

function CopyPath({ text, token }: { text: string; token: string }) {
  const [copied, setCopied] = useState(false)
  const [manual, setManual] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
    } catch {
      setManual(true)
    }
  }
  return (
    <div className="crew-actions">
      <p className="crew-preview">{text}</p>
      <Button onClick={copy}>{copied ? 'Copied' : 'Copy text'}</Button>
      {manual && <textarea className="crew-draft" readOnly value={text} aria-label="Draft text" onFocus={(e) => e.target.select()} />}
      <Link className="btn btn-secondary" to={`/s/${encodeURIComponent(token)}/fallback`}>Other ways to share</Link>
    </div>
  )
}

export default function Approve() {
  const token = useParams().token ?? ''
  const navigate = useNavigate()
  const { view: invite, error: inviteError } = useInvite(token)
  const memberId = invite?.member.id
  const [data, setData] = useState<ApproveView>()
  const [fail, setFail] = useState<ApiError>()
  const [retry, setRetry] = useState(0)
  const [checked, setChecked] = useState<Record<string, boolean>>({})
  const [timing, setTiming] = useState<Timing>('queue')
  const [active, setActive] = useState('')
  const [locked, setLocked] = useState<Set<string>>(new Set())
  const [outcomes, setOutcomes] = useState<Record<string, Outcome>>({})
  const [posting, setPosting] = useState(false)
  const [added, setAdded] = useState(false)
  const [message, setMessage] = useState('')
  const [windowGone, setWindowGone] = useState(false)
  const [checking, setChecking] = useState(false)
  const [checkedNote, setCheckedNote] = useState('')
  const [voice, setVoice] = useState(false)
  const [kept, setKept] = useState(false)
  const [rewriting, setRewriting] = useState(false)
  const [bounced, setBounced] = useState(false)
  const tune = useRef<HTMLButtonElement>(null)
  const texts = useRef<Record<string, string>>({})
  const inflight = useRef(false)
  const results = useRef<HTMLUListElement>(null)
  const reconnect = `/auth/buffer/start?return_to=/s/${encodeURIComponent(token)}/approve`

  useEffect(() => {
    if (!memberId || invite?.foreign) return
    let live = true
    setFail(undefined)
    connect(memberId, token).then(
      (d) => {
        if (!live) return
        setTriedReconnect(token, false)
        setData(d)
        setChecked(Object.fromEntries(d.channels.map((c) => [c.id, c.eligible && c.checked])))
        setTiming(d.timing[0] ?? 'queue')
      },
      (e) => live && setFail(e instanceof ApiError ? e : new ApiError(0, 'error', String(e))),
    )
    return () => {
      live = false
    }
  }, [memberId, token, invite?.foreign, retry])

  // A channel added in Buffer after connecting is hidden by a one-hour cache; this asks Buffer again.
  const refreshChannels = async () => {
    if (!memberId) return
    setChecking(true)
    setCheckedNote('Checking Buffer…')
    try {
      const d = await refreshMyChannels(memberId, token)
      setData(d)
      adopt(d.drafts)
      // Keep the person's ticks; a channel that is new takes its default.
      setChecked((prev) => Object.fromEntries(d.channels.map((c) => [c.id, c.eligible && (c.id in prev ? prev[c.id] : c.checked)])))
      setCheckedNote(`Checked Buffer: ${d.channels.length} ${d.channels.length === 1 ? 'channel' : 'channels'}.`)
    } catch {
      setCheckedNote('Could not reach Buffer, try again.')
    } finally {
      setChecking(false)
    }
  }

  // Buffer rejected the token: reconnect once, and if that lands on another 401 say so.
  const goReconnect = () => {
    if (triedReconnect(token)) {
      setTriedReconnect(token, false)
      setBounced(true)
      return
    }
    setTriedReconnect(token, true)
    window.location.assign(reconnect)
  }

  useEffect(() => {
    if (fail?.status === 401) goReconnect()
    if (fail?.code === 'verify_first') navigate(`/s/${encodeURIComponent(token)}`)
    // goReconnect only reads token and reconnect, both already listed
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fail, reconnect, navigate, token])

  const { fresh, polling, restart, adopt } = useDrafts(memberId, token, !!data && !data.voicedReady)

  useEffect(() => {
    if (Object.keys(outcomes).length) results.current?.focus()
  }, [outcomes])

  useEffect(() => {
    if (!kept) return
    const t = window.setTimeout(() => setKept(false), KEPT_FOR)
    return () => window.clearTimeout(t)
  }, [kept])

  if (inviteError) return <Dead code={inviteError.code} />
  if (invite?.foreign) return <Foreign releaseUrl={invite.launch.releaseUrl} />
  if (fail?.code === 'already_claimed') {
    return (
      <CrewFrame>
        <h1 className="crew-title">This invite is taken</h1>
        <p className="crew-lede">{fail.message}</p>
      </CrewFrame>
    )
  }
  if (fail && fail.status !== 502 && fail.status !== 401 && fail.code !== 'verify_first') return <Dead code={fail.code} />

  const channels = data?.channels ?? []
  const drafts = (fresh ?? data?.drafts ?? []).filter((d) => channels.some((c) => c.id === d.channelId && c.eligible))
  const shown = drafts.filter((d) => checked[d.channelId])
  const current = shown.find((d) => d.channelId === active) ?? shown[0]
  // The network's name sits beside the account name, unless the account is already called that.
  const metaOf = (c?: { name: string; service: string }) => {
    const n = c ? serviceName(c.service) : ''
    return c && n.toLowerCase() !== c.name.toLowerCase() ? n : ''
  }
  const done = (id: string) => outcomes[id]?.status === 'queued' || outcomes[id]?.status === 'draft'
  // Each draft is counted against its own network; the limits differ per channel.
  const limitOf = (d: { network: string }) => LIMITS[d.network.toLowerCase()]
  const eligible = channels.some((c) => c.eligible)
  const tabKey = (e: KeyboardEvent, i: number) => {
    const to = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? shown.length - 1 : -1
    if (to < 0) return
    e.preventDefault()
    const next = shown[(to + shown.length) % shown.length]
    setActive(next.channelId)
    document.getElementById(`tab-${next.channelId}`)?.focus()
  }

  async function submit() {
    if (!data || !memberId || inflight.current) return
    const picks = shown.filter((d) => !done(d.channelId))
    if (!picks.length) return
    inflight.current = true
    setPosting(true)
    setMessage('')
    try {
      const r = await post(memberId, token, picks.map((d) => ({
        channelId: d.channelId, text: texts.current[d.id] ?? d.text, timing, assetUrl: invite?.card?.url ?? '',
      })))
      const all = { ...outcomes, ...Object.fromEntries(r.outcomes.map((o) => [o.channelId, o])) }
      setOutcomes(all)
      if (!r.outcomes.length) setMessage(BUSY)
      else if (r.outcomes.every((o) => o.status !== 'failed')) {
        setAdded(true)
        navigate(`/s/${encodeURIComponent(token)}/done`, { state: { outcomes: Object.values(all) } })
      }
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) goReconnect()
      else if (e instanceof ApiError && e.code === 'window_closed') {
        setMessage(e.message)
        setTiming('queue')
        setWindowGone(true)
      } else setMessage(e instanceof ApiError && e.status !== 502 ? e.message : BUSY)
    } finally {
      inflight.current = false
      setPosting(false)
    }
  }

  async function saveVoice(edit: VoiceEdit) {
    if (!memberId) return
    const r = await updateVoice(memberId, token, edit)
    setData((d) => d && { ...d, voice: { ...d.voice, ...edit }, voiceLine: r.voiceLine })
    setKept(drafts.some((d) => d.edited || (texts.current[d.id] ?? d.text) !== d.text))
    setRewriting(true)
    restart(drafts)
  }

  function closeVoice() {
    setVoice(false)
    tune.current?.focus()
  }

  function save(draftId: string, text: string) {
    if (!memberId) return
    saveDraft(memberId, token, draftId, text).catch((e) => {
      if (e instanceof ApiError && e.code === 'already_queued') setLocked((l) => new Set(l).add(draftId))
    })
  }

  if (bounced) {
    return (
      <CrewFrame footer>
        <Alert tone="error">Buffer did not accept the connection. Reconnect to try again, or share another way.</Alert>
        <div className="crew-actions">
          <a className="btn btn-primary" href={reconnect}>Reconnect Buffer</a>
          <Link className="btn btn-secondary" to={`/s/${encodeURIComponent(token)}/fallback`}>Other ways to share</Link>
        </div>
      </CrewFrame>
    )
  }

  if (fail?.status === 502) {
    return (
      <CrewFrame footer>
        <Alert tone="error">
          {BUSY}. <Button variant="secondary" onClick={() => setRetry((n) => n + 1)}>Try again</Button>
        </Alert>
      </CrewFrame>
    )
  }

  return (
    <CrewFrame version={invite?.launch.version} footer>
      {invite?.card && (
        <span className="eyebrow crew-eyebrow">
          {invite.launch.project} {invite.launch.version}
        </span>
      )}
      <h1 className="crew-title">{invite?.launch.title}</h1>
      {invite && (
        invite.card ? (
          <img className="card card-img" src={invite.card.url} alt={invite.card.alt} />
        ) : (
          <Pass
            eyebrow={invite.launch.project}
            name={invite.member.name}
            meta={invite.member.role}
            stub={{ label: 'Pass', value: invite.launch.version || '·', date: passDate(invite.launch.launchAt) }}
          />
        )
      )}
      <div className="perf perf-h crew-perf" aria-hidden="true" />
      {message && <Alert tone="error">{message}</Alert>}
      <div className="crew-stack" aria-busy={!data || undefined}>
        {!data ? (
          <Loader label="Building your post" hint="Nothing is queued yet. You see the draft first, and nothing posts until you tap approve." />
        ) : !eligible ? (
          <CopyPath text={data.drafts[0]?.text ?? invite?.copyText ?? ''} token={token} />
        ) : (
          <>
            {shown.length > 1 && (
              <div role="tablist" aria-label="Channel drafts" className="crew-tabs">
                {shown.map((d, i) => (
                  <button
                    key={d.channelId}
                    id={`tab-${d.channelId}`}
                    role="tab"
                    type="button"
                    className="crew-tab"
                    aria-selected={d.channelId === current?.channelId}
                    aria-controls={`panel-${d.channelId}`}
                    tabIndex={d.channelId === current?.channelId ? 0 : -1}
                    onClick={() => setActive(d.channelId)}
                    onKeyDown={(e) => tabKey(e, i)}
                  >
                    <ServiceIcon service={channels.find((c) => c.id === d.channelId)?.service ?? ''} />
                    <span>{channels.find((c) => c.id === d.channelId)?.name}</span>{' '}
                    {metaOf(channels.find((c) => c.id === d.channelId)) && (
                      <span className="crew-tab-meta">{metaOf(channels.find((c) => c.id === d.channelId))}</span>
                    )}
                  </button>
                ))}
              </div>
            )}
            {drafts.map((d) => (
              <div
                key={d.id}
                id={`panel-${d.channelId}`}
                role={shown.length > 1 ? 'tabpanel' : undefined}
                aria-labelledby={shown.length > 1 ? `tab-${d.channelId}` : undefined}
                hidden={d.channelId !== current?.channelId}
              >
                <DraftBox
                  id={d.id}
                  serverText={d.text}
                  fromModel={d.fromModel && !(rewriting && polling)}
                  waiting={polling}
                  limit={limitOf(d)}
                  countFirst={d.thread[0]}
                  readOnly={locked.has(d.id)}
                  lockedNote="Already in your Buffer, edit it there"
                  onText={(t) => {
                    texts.current[d.id] = t
                    setKept(false)
                  }}
                  onSave={(t) => save(d.id, t)}
                />
              </div>
            ))}
          </>
        )}
        {data && (
          <fieldset className="crew-fieldset">
            <legend>Post to</legend>
            <div className="crew-chips">
              {channels.map((c) => (
                <Chip
                  key={c.id}
                  label={c.name}
                  meta={metaOf(c) || undefined}
                  icon={<ServiceIcon service={c.service} />}
                  checked={!!checked[c.id]}
                  disabled={!c.eligible}
                  reason={c.reason}
                  onChange={(v) => setChecked((s) => ({ ...s, [c.id]: v }))}
                />
              ))}
              <IconButton icon="refresh" label="Refresh channels" busy={checking} className="crew-refresh" onClick={refreshChannels} />
            </div>
            <p className="crew-note crew-fine crew-checked" role="status">
              {checkedNote}
            </p>
          </fieldset>
        )}
      </div>
      {Object.keys(outcomes).length > 0 && <Results outcomes={Object.values(outcomes)} channels={channels} listRef={results} />}
      <div className="crew-actions">
        {data?.needsReconnect ? (
          <a className="btn btn-primary" href={reconnect}>Reconnect Buffer</a>
        ) : (
          (!data || eligible) && (
            <Button busy={!data || posting} onClick={submit} disabled={!!data && shown.every((d) => done(d.channelId))}>
              {added ? 'Added' : data && shown.length === 0 ? 'Pick a channel' : 'Add to my queue'}
            </Button>
          )
        )}
      </div>
      {data && eligible && !data.needsReconnect && (
        <fieldset className="crew-fieldset">
          <legend>When it goes out</legend>
          <div className="crew-options">
            {data.timing.filter((t) => !(windowGone && t === 'window')).map((t) => (
              <label key={t} className="crew-option">
                <input
                  className="crew-radio-input"
                  type="radio"
                  name="timing"
                  aria-labelledby={`timing-${t}`}
                  aria-describedby={`timing-${t}-hint`}
                  checked={timing === t}
                  onChange={() => setTiming(t)}
                />
                <span className="crew-option-body">
                  <span id={`timing-${t}`} className="crew-option-title">{TIMING[t]}</span>
                  <span id={`timing-${t}-hint`} className="crew-option-hint">{TIMING_HINT[t]}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
      )}
      {data && (
        <>
          <p className="crew-note crew-fine">
            {data.voiceLine}{' '}
            <button type="button" className="crew-inline" aria-expanded={voice} ref={tune} onClick={() => setVoice((v) => !v)}>tune</button>
          </p>
          {kept && <p className="crew-note" role="status">your edits are kept; new drafts will use this voice</p>}
          {voice && <VoiceCard profile={data.voice} onSave={saveVoice} onClose={closeVoice} />}
        </>
      )}
      <TrustStrip memberId={memberId} token={token} />
    </CrewFrame>
  )
}
