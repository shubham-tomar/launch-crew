import { useEffect, useState } from 'react'
import { listRepos, patchRepo, type ApiError, type Repo } from '../api'
import CardBrand from './CardBrand'
import Alert from '../components/Alert'
import Button from '../components/Button'
import { SkeletonPanel } from '../components/Skeleton'
import Frame from './Frame'
import { asError } from './format'
import './repos.css'

const INSTALL = '/github/install/start'

const plus = (
  <svg viewBox="0 0 14 14" aria-hidden="true">
    <path d="M7 2v10M2 7h10" />
  </svg>
)

const repoIcon = (
  <svg viewBox="0 0 20 20" aria-hidden="true">
    <path d="M5 3.5h8.5a1.5 1.5 0 0 1 1.5 1.5v11H6.5A1.5 1.5 0 0 1 5 14.5V3.5z" />
    <path d="M5 14.5A1.5 1.5 0 0 1 6.5 13H15" />
  </svg>
)

type SwitchProps = { label: string; name: string; checked: boolean; disabled?: boolean; onChange: (v: boolean) => void }

function Switch({ label, name, checked, disabled, onChange }: SwitchProps) {
  return (
    <label className="rp-switch">
      <input
        type="checkbox"
        role="switch"
        checked={checked}
        disabled={disabled}
        aria-label={`${name}: ${label.toLowerCase()}`}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="rp-track" aria-hidden="true">
        <span className="rp-thumb" />
      </span>
      <span className="rp-switch-text">{label}</span>
    </label>
  )
}

export default function Repos() {
  const [repos, setRepos] = useState<Repo[]>()
  const [error, setError] = useState<ApiError>()
  const [attempt, setAttempt] = useState(0)
  // Per-row state: which repo has a PATCH in flight, and the last failure for each.
  const [saving, setSaving] = useState<Record<string, boolean>>({})
  const [rowError, setRowError] = useState<Record<string, string>>({})

  useEffect(() => {
    let live = true
    listRepos()
      .then((r) => live && setRepos(r))
      .catch((e) => live && setError(asError(e)))
    return () => {
      live = false
    }
  }, [attempt])

  const retry = () => {
    setError(undefined)
    setAttempt((n) => n + 1)
  }

  // Flip at once, hold the row while it saves, and put it back with the reason if it fails.
  async function toggle(r: Repo, p: { enabled?: boolean; allowPrerelease?: boolean }) {
    const put = (next: Repo) => setRepos((rs) => rs?.map((x) => (x.id === next.id ? next : x)))
    setRowError((m) => ({ ...m, [r.id]: '' }))
    setSaving((m) => ({ ...m, [r.id]: true }))
    put({ ...r, ...p })
    try {
      put(await patchRepo(r.id, p))
    } catch (e) {
      put(r)
      setRowError((m) => ({ ...m, [r.id]: asError(e).message }))
    } finally {
      setSaving((m) => ({ ...m, [r.id]: false }))
    }
  }

  const add = (
    <a className="btn btn-primary rp-add" href={INSTALL}>
      {plus}
      Add a repo
    </a>
  )

  return (
    <Frame>
      <div className="rp-head">
        <div className="rp-head-text">
          <h1 className="rp-title">Repos</h1>
          <p className="rp-lede">
            Launch Crew watches releases on the repos you add. A published release becomes a draft launch here; drafts and prereleases are
            skipped unless you allow them.
          </p>
        </div>
        {add}
      </div>
      {error && (
        <Alert tone="error">
          {error.message}{' '}
          {!repos && <Button variant="secondary" onClick={retry}>Retry</Button>}
        </Alert>
      )}
      {!repos ? (
        !error && (
          <div role="status" aria-busy="true" className="rp-list">
            <span className="visually-hidden">Loading your repos</span>
            <SkeletonPanel lines={1} />
            <SkeletonPanel lines={1} />
          </div>
        )
      ) : repos.length === 0 ? (
        <section className="rp-empty" aria-label="No repos yet">
          <span className="rp-icon" aria-hidden="true">
            {repoIcon}
          </span>
          <h2>No repos yet</h2>
          <p>Add a repo and every release you publish in it creates a draft launch for you to review. Nothing posts until you press Launch.</p>
          {add}
        </section>
      ) : (
        <ul className="rp-list">
          {repos.map((r) => (
            <li key={r.id} className="rp-repo">
              <span className="rp-icon" aria-hidden="true">
                {repoIcon}
              </span>
              <span className="rp-name">
                <strong>{r.fullName}</strong>
                <span>{r.enabled ? (r.allowPrerelease ? 'Watching releases and prereleases' : 'Watching releases') : 'Paused'}</span>
              </span>
              <span className="rp-switches" aria-busy={saving[r.id] || undefined}>
                <Switch label="Watch releases" name={r.fullName} checked={r.enabled} disabled={saving[r.id]} onChange={(v) => toggle(r, { enabled: v })} />
                <Switch
                  label="Include prereleases"
                  name={r.fullName}
                  checked={r.allowPrerelease}
                  disabled={saving[r.id]}
                  onChange={(v) => toggle(r, { allowPrerelease: v })}
                />
              </span>
              <CardBrand repo={r} onSaved={(next) => setRepos((rs) => rs?.map((x) => (x.id === next.id ? next : x)))} />
              {rowError[r.id] && (
                <span className="rp-row-error" role="alert">
                  {rowError[r.id]} Try again.
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </Frame>
  )
}
