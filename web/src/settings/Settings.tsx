import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ApiError, deleteMe, getMe, optInMe, optOutMe, optOutWithToken, type Me } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import SchemeSwitch from '../components/SchemeSwitch'
import Shell from '../components/Shell'
import Loader from '../components/Loader'
import NavPill from '../components/NavPill'
import NotifySettings from './NotifySettings'
import './settings.css'

type Phase = 'idle' | 'confirm' | 'busy'
// How far an opt-out made on this page reached: every launch, or the token's launch only.
type Scope = 'all' | 'launch'

const isEmpty = (m: Me) => m.githubLogins.length === 0 && !m.bufferConnected && m.launches === 0 && m.crewRows === 0

function Stored({ me }: { me: Me }) {
  const count = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
  return (
    <ul className="settings-list">
      {me.githubLogins.length > 0 && <li>GitHub: {me.githubLogins.join(', ')}</li>}
      {me.bufferConnected && <li>A connected Buffer account (token stored encrypted)</li>}
      {me.launches > 0 && <li>{count(me.launches, 'launch you run', 'launches you run')}</li>}
      {me.crewRows > 0 && <li>{count(me.crewRows, 'crew invite and its drafts', 'crew invites and their drafts')}</li>}
    </ul>
  )
}

export default function Settings() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const [me, setMe] = useState<Me>()
  const [loadFailed, setLoadFailed] = useState(false)
  const [optedOut, setOptedOut] = useState<Scope>()
  const [undone, setUndone] = useState(false)
  const [deleted, setDeleted] = useState(false)
  const [optPhase, setOptPhase] = useState<Phase>('idle')
  const [phase, setPhase] = useState<Phase>('idle')
  const [optFailed, setOptFailed] = useState(false)
  const [noGitHub, setNoGitHub] = useState(false)
  const [undoFailed, setUndoFailed] = useState(false)
  const [delFailed, setDelFailed] = useState(false)

  useEffect(() => {
    let live = true
    getMe().then(
      (m) => live && setMe(m),
      () => live && setLoadFailed(true),
    )
    return () => {
      live = false
    }
  }, [])

  const signedIn = me?.signedIn ?? false
  const viaToken = me !== undefined && !signedIn && token !== ''
  const inviteLink = `/s/${encodeURIComponent(token)}`

  async function optOut() {
    setOptPhase('busy')
    setOptFailed(false)
    setUndone(false)
    try {
      // A comment link always wins: the token names exactly whom to opt out, and
      // the server decides whether it reaches one launch or all of them.
      const r = await (token ? optOutWithToken(token) : optOutMe())
      setOptedOut(r.scope)
    } catch (e) {
      setOptFailed(true)
      setNoGitHub(e instanceof ApiError && e.code === 'no_github')
    }
    setOptPhase('idle')
  }

  async function undo() {
    setUndoFailed(false)
    try {
      await optInMe()
      setOptedOut(undefined)
      setUndone(true)
      if (me) setMe({ ...me, optedOut: false })
    } catch {
      setUndoFailed(true)
    }
  }

  async function wipe() {
    setPhase('busy')
    setDelFailed(false)
    try {
      await deleteMe()
      setDeleted(true)
    } catch {
      setDelFailed(true)
      setPhase('idle')
    }
  }

  let body
  if (loadFailed) {
    body = <Alert tone="error">We could not load your settings. Try again in a moment.</Alert>
  } else if (!me) {
    body = <Loader label="Loading your settings" compact />
  } else if (deleted) {
    body = <Alert tone="success">Deleted. Your tokens and drafts are gone.</Alert>
  } else {
    // What the opt-out covers once it is done: the server's answer, or the stored one.
    const done: Scope | undefined = optedOut ?? (signedIn && me.optedOut ? 'all' : undefined)
    body = (
      <>
        {signedIn && (isEmpty(me) ? <p>Nothing stored about you yet.</p> : <Stored me={me} />)}
        {signedIn && me.notify && <NotifySettings initial={me.notify} emailResult={params.get('email')} />}
        {!signedIn && !viaToken && (
          <p>
            <a className="btn btn-primary" href="/auth/buffer/start?return_to=/settings">
              Sign in to see what we store
            </a>
          </p>
        )}
        {(signedIn || viaToken) && (
          <section className="settings-actions" aria-label="Your choices">
            {done === 'all' && (
              <>
                <Alert tone="success">Opted out. We will never comment on your PRs again.</Alert>
                <Button variant="secondary" onClick={undo}>
                  Undo opt-out
                </Button>
              </>
            )}
            {done === 'launch' && (
              <Alert tone="success">
                Opted out. We will not comment on your PRs for this launch. To stop comments everywhere, verify it's
                you with GitHub from <a href={inviteLink}>your invite link</a>, then opt out here while signed in.
              </Alert>
            )}
            {undone && <Alert tone="success">Opt-out undone. Future launches can invite you again.</Alert>}
            {done === undefined && optPhase === 'idle' && (
              <Button variant="secondary" onClick={() => setOptPhase('confirm')}>
                {token ? 'Opt out of comments for this launch' : 'Opt out of all comments'}
              </Button>
            )}
            {done === undefined && optPhase !== 'idle' && (
              <>
                <p className="settings-note">
                  {token
                    ? 'This stops comments for this launch only. To stop them everywhere, verify it\'s you with GitHub from your invite link, then opt out here while signed in.'
                    : 'We will stop commenting on your PRs in every future launch. You can undo this here while signed in.'}
                </p>
                <Button variant="primary" busy={optPhase === 'busy'} disabled={optPhase === 'busy'} onClick={optOut}>
                  Yes, opt out
                </Button>
              </>
            )}
            {optFailed && (
              <Alert tone="error">
                {noGitHub
                  ? 'We found no GitHub account to opt out. Use the link from a PR comment.'
                  : 'We could not opt you out. Try again in a moment.'}
              </Alert>
            )}
            {undoFailed && <Alert tone="error">We could not undo the opt-out. Try again in a moment.</Alert>}
            {signedIn && phase === 'idle' && (
              <Button variant="secondary" onClick={() => setPhase('confirm')}>
                Delete everything
              </Button>
            )}
            {signedIn && phase !== 'idle' && (
              <>
                <p className="settings-note">This removes your tokens, drafts and crew invites. It cannot be undone.</p>
                <Button variant="primary" busy={phase === 'busy'} disabled={phase === 'busy'} onClick={wipe}>
                  Yes, delete everything
                </Button>
              </>
            )}
            {delFailed && (
              <Alert tone="error">
                We could not delete everything; email us{me.supportEmail ? ` at ${me.supportEmail}` : ''} and we will finish it by hand.
              </Alert>
            )}
          </section>
        )}
      </>
    )
  }

  return (
    <Shell width="person">
      <div className="settings-page">
        <NavPill home="/" />
        <section className="panel" aria-labelledby="settings-title">
          <h1 className="settings-title" id="settings-title">Settings</h1>
          {body}
        </section>
        <footer className="settings-footer">
          <SchemeSwitch />
          <span>Built on the Buffer API</span>
        </footer>
      </div>
    </Shell>
  )
}
