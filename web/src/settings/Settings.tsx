import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ApiError, deleteMe, getMe, optOutMe, optOutWithToken, type Me } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import SchemeSwitch from '../components/SchemeSwitch'
import Shell from '../components/Shell'
import Loader from '../components/Loader'
import NavPill from '../components/NavPill'
import './settings.css'

type Phase = 'idle' | 'confirm' | 'busy'

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
  const token = useSearchParams()[0].get('token') ?? ''
  const [me, setMe] = useState<Me>()
  const [loadFailed, setLoadFailed] = useState(false)
  const [optedOut, setOptedOut] = useState(false)
  const [deleted, setDeleted] = useState(false)
  const [phase, setPhase] = useState<Phase>('idle')
  const [optFailed, setOptFailed] = useState(false)
  const [noGitHub, setNoGitHub] = useState(false)
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

  async function optOut() {
    setOptFailed(false)
    try {
      // A comment link always wins: the token names exactly whom to opt out.
      await (token ? optOutWithToken(token) : optOutMe())
      setOptedOut(true)
    } catch (e) {
      setOptFailed(true)
      setNoGitHub(e instanceof ApiError && e.code === 'no_github')
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
    const done = optedOut || (signedIn && me.optedOut)
    body = (
      <>
        {signedIn && (isEmpty(me) ? <p>Nothing stored about you yet.</p> : <Stored me={me} />)}
        {!signedIn && !viaToken && (
          <p>
            <a className="btn btn-primary" href="/auth/buffer/start?return_to=/settings">
              Sign in to see what we store
            </a>
          </p>
        )}
        {(signedIn || viaToken) && (
          <section className="settings-actions" aria-label="Your choices">
            {done ? (
              <Alert tone="success">Opted out. We will never comment on your PRs again.</Alert>
            ) : (
              <Button variant="secondary" onClick={optOut}>
                Opt out of all comments
              </Button>
            )}
            {optFailed && (
              <Alert tone="error">
                {noGitHub
                  ? 'We found no GitHub account to opt out. Use the link from a PR comment.'
                  : 'We could not opt you out. Try again in a moment.'}
              </Alert>
            )}
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
