import { useEffect, useState, type FormEvent } from 'react'
import { patchRepo, previewRepoCard, type Repo } from '../api'
import Button from '../components/Button'
import Icon from '../components/Icon'
import { asError } from './format'

// Accents that read on the ink card (at least 4.5:1), in DESIGN.md's palette.
const SWATCHES = ['#ADA9FF', '#58D3AE', '#D9A520', '#EC8669', '#7FB2FF']
const DEFAULT_ACCENT = '#ADA9FF'
const HEX = /^#[0-9a-f]{6}$/i

const owner = (fullName: string) => fullName.split('/')[0] ?? ''
const PREVIEW_DELAY = 600

const httpsUrl = (s: string) => {
  try {
    return new URL(s).protocol === 'https:'
  } catch {
    return false
  }
}

type Preview = { png?: string; error?: string; loading: boolean }

// Redraws the sample card a moment after the logo or accent stops changing, only while the fold is open.
function useCardPreview(repoId: string, open: boolean, logo: string, accent: string): Preview {
  const [state, setState] = useState<Preview>({ loading: false })
  const ready = (logo === '' || httpsUrl(logo)) && (accent === '' || HEX.test(accent))
  useEffect(() => {
    if (!open || !ready) return
    let live = true
    const t = window.setTimeout(() => {
      setState((s) => ({ ...s, loading: true, error: undefined }))
      previewRepoCard(repoId, { logoUrl: logo, accent })
        .then((r) => live && setState({ png: r.png, loading: false }))
        .catch((e) => live && setState((s) => ({ ...s, loading: false, error: asError(e).message })))
    }, PREVIEW_DELAY)
    return () => {
      live = false
      window.clearTimeout(t)
    }
  }, [repoId, open, ready, logo, accent])
  return state
}

/** The repo's card branding: a logo (the owner's GitHub avatar when empty) and an accent. */
export default function CardBrand({ repo, onSaved }: { repo: Repo; onSaved: (r: Repo) => void }) {
  const [logo, setLogo] = useState(repo.logoUrl)
  const [accent, setAccent] = useState(repo.accent)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [open, setOpen] = useState(false)
  const card = useCardPreview(repo.id, open, logo.trim(), accent.trim())

  const shown = HEX.test(accent) ? accent : DEFAULT_ACCENT
  const dirty = logo.trim() !== repo.logoUrl || accent.trim().toUpperCase() !== repo.accent.toUpperCase()
  const badHex = accent.trim() !== '' && !HEX.test(accent.trim())
  const id = `cb-${repo.id}`
  const preview = logo.trim() || `https://github.com/${encodeURIComponent(owner(repo.fullName))}.png?size=88`

  async function save(e: FormEvent) {
    e.preventDefault()
    if (badHex || saving) return
    setSaving(true)
    setError('')
    setSaved(false)
    try {
      const next = await patchRepo(repo.id, { logoUrl: logo.trim(), accent: accent.trim() })
      setLogo(next.logoUrl)
      setAccent(next.accent)
      setSaved(true)
      onSaved(next)
    } catch (err) {
      setError(asError(err).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <details className="rp-brand" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>
        <span className="rp-brand-tile" style={{ boxShadow: `0 0 0 2px ${shown}` }} aria-hidden="true">
          <img src={preview} alt="" loading="lazy" />
        </span>
        <span>Card branding</span>
        <Icon name="chevron" className="rp-brand-chevron" />
      </summary>
      <form className="rp-brand-form" onSubmit={save} aria-label={`Card branding for ${repo.fullName}`}>
        <div className="rp-brand-field">
          <label htmlFor={`${id}-logo`}>Logo URL</label>
          <input
            id={`${id}-logo`}
            className="field"
            type="url"
            inputMode="url"
            placeholder="https://…/logo.png"
            value={logo}
            onChange={(e) => setLogo(e.target.value)}
            aria-describedby={`${id}-logo-hint`}
          />
          <span id={`${id}-logo-hint`} className="rp-brand-hint">
            Square works best. Leave empty to use {owner(repo.fullName)}'s GitHub avatar.
          </span>
        </div>
        <fieldset className="rp-brand-field">
          <legend>Accent</legend>
          <div className="rp-swatches">
            {SWATCHES.map((c) => (
              <button
                key={c}
                type="button"
                className="rp-swatch"
                style={{ backgroundColor: c }}
                aria-label={`Use ${c}`}
                aria-pressed={shown.toUpperCase() === c}
                onClick={() => setAccent(c)}
              />
            ))}
            <input
              className="field rp-hex"
              aria-label="Accent hex colour"
              placeholder={DEFAULT_ACCENT}
              maxLength={7}
              value={accent}
              aria-invalid={badHex || undefined}
              onChange={(e) => setAccent(e.target.value)}
            />
          </div>
          <span className="rp-brand-hint">Dark colours are refused: the card is ink, so the accent has to read on it.</span>
        </fieldset>
        <figure className="rp-preview" aria-busy={card.loading || undefined}>
          {card.png ? (
            <img src={card.png} alt={`Sample card for ${repo.fullName} with this branding`} className={card.loading ? 'rp-preview-stale' : undefined} />
          ) : (
            <span className="rp-preview-empty skeleton-line" aria-hidden="true" />
          )}
          <figcaption className="rp-brand-hint">
            {card.error ? <span className="rp-row-error">{card.error}</span> : card.loading ? 'Drawing the card…' : 'Preview with sample changes. Nothing is saved until you press Save.'}
          </figcaption>
        </figure>
        <div className="rp-brand-actions">
          <Button type="submit" disabled={!dirty || badHex} busy={saving}>
            Save branding
          </Button>
          {saved && !dirty && (
            <span className="rp-brand-saved" role="status">
              <Icon name="check" /> Saved. New cards use it.
            </span>
          )}
          {badHex && <span className="rp-row-error">Use a hex colour like #58D3AE.</span>}
          {error && (
            <span className="rp-row-error" role="alert">
              {error}
            </span>
          )}
        </div>
      </form>
    </details>
  )
}
