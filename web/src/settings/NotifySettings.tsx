import { useState, type FormEvent } from 'react'
import { ApiError, clearNotifyEmail, setNotify, startNotifyEmail, type Notify } from '../api'
import Alert from '../components/Alert'
import Button from '../components/Button'
import ChoiceRow from '../components/ChoiceRow'

type Props = {
  initial: Notify
  /** ?email= from the verification link: verified or expired. */
  emailResult: string | null
}

// How a captain hears that a release made a launch draft: a GitHub @mention by
// default, a line in the release notes, or email to an address they verified.
export default function NotifySettings({ initial, emailResult }: Props) {
  const [n, setN] = useState(initial)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')
  // Email ticked but no verified address yet: show the address form.
  const [askEmail, setAskEmail] = useState(false)
  const [address, setAddress] = useState(initial.emailPending || '')
  const [sending, setSending] = useState(false)

  async function save(next: { github: boolean; releaseNotes: boolean; email: boolean }) {
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      setN(await setNotify(next))
      setSaved(true)
    } catch {
      setError('We could not save that. Try again in a moment.')
    }
    setSaving(false)
  }

  const verified = n.emailAddress !== ''
  const emailOn = n.email || askEmail || (!verified && n.emailPending !== '')

  function toggleEmail() {
    if (emailOn) {
      setAskEmail(false)
      if (n.email) void save({ github: n.github, releaseNotes: n.releaseNotes, email: false })
      return
    }
    if (verified) void save({ github: n.github, releaseNotes: n.releaseNotes, email: true })
    else setAskEmail(true)
  }

  async function sendLink(e: FormEvent) {
    e.preventDefault()
    setSending(true)
    setError('')
    try {
      setN(await startNotifyEmail(address.trim()))
      setAskEmail(false)
    } catch (err) {
      const code = err instanceof ApiError ? err.code : ''
      setError(
        code === 'bad_email'
          ? 'Enter one email address, like you@example.com.'
          : code === 'rate_limited'
            ? 'We just sent a link. Wait a minute before asking again.'
            : 'We could not send the link. Try again in a moment.',
      )
    }
    setSending(false)
  }

  async function removeEmail() {
    setError('')
    try {
      setN(await clearNotifyEmail())
      setAskEmail(false)
      setAddress('')
    } catch {
      setError('We could not remove the address. Try again in a moment.')
    }
  }

  const emailHint = !n.mailAvailable
    ? 'Email is not set up on this server yet.'
    : verified
      ? `To ${n.emailAddress}.`
      : 'We ask you to confirm the address first.'

  return (
    <section className="settings-notify" aria-labelledby="settings-notify-title">
      <h2 className="settings-subtitle" id="settings-notify-title">
        When a release makes a draft
      </h2>
      <p className="settings-note">Each release of your repos makes a launch draft. Choose how we tell you.</p>
      {emailResult === 'verified' && <Alert tone="success">Email confirmed. We will email you when a release makes a draft.</Alert>}
      {emailResult === 'expired' && <Alert tone="error">That link expired or was already used. Send a new one below.</Alert>}
      <div className="settings-choices">
        <ChoiceRow
          type="checkbox"
          name="notify"
          value="github"
          label="@mention me on GitHub"
          hint="A short comment on your latest PR in the release."
          checked={n.github}
          disabled={saving}
          onChange={() => save({ github: !n.github, releaseNotes: n.releaseNotes, email: n.email })}
        />
        <ChoiceRow
          type="checkbox"
          name="notify"
          value="release-notes"
          label="Add a link to the release notes"
          hint="One line under the notes. The crew list replaces it when you launch."
          checked={n.releaseNotes}
          disabled={saving}
          onChange={() => save({ github: n.github, releaseNotes: !n.releaseNotes, email: n.email })}
        />
        <ChoiceRow
          type="checkbox"
          name="notify"
          value="email"
          label="Email me"
          hint={emailHint}
          checked={emailOn && n.mailAvailable}
          disabled={saving || !n.mailAvailable}
          onChange={toggleEmail}
        />
      </div>
      {n.mailAvailable && n.emailPending !== '' && !askEmail && (
        <p className="settings-note">
          We sent a link to {n.emailPending}. Open it to turn email on.{' '}
          <button type="button" className="settings-inline" onClick={() => setAskEmail(true)}>
            Send again or change it
          </button>
        </p>
      )}
      {n.mailAvailable && askEmail && (
        <form className="settings-email" onSubmit={sendLink}>
          <label htmlFor="notify-email">Your email</label>
          <div className="settings-email-row">
            <input
              id="notify-email"
              className="field"
              type="email"
              autoComplete="email"
              required
              value={address}
              onChange={(e) => setAddress(e.target.value)}
            />
            <Button variant="primary" type="submit" busy={sending} disabled={sending}>
              Send link
            </Button>
          </div>
        </form>
      )}
      {verified && (
        <p className="settings-note">
          <button type="button" className="settings-inline" onClick={removeEmail}>
            Remove {n.emailAddress}
          </button>
        </p>
      )}
      {saved && !error && (
        <p className="settings-note" role="status">
          Saved.
        </p>
      )}
      {error && <Alert tone="error">{error}</Alert>}
    </section>
  )
}
