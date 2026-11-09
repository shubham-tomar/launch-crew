import { useEffect, useState, type FormEvent } from 'react'
import { patchRepo, previewRepoCard, type Repo } from '../api'
import Button from '../components/Button'
import Icon from '../components/Icon'
import { asError } from './format'

// Accents that read on the ink card (at least 4.5:1), in DESIGN.md's palette.
const ACCENTS = ['#ADA9FF', '#58D3AE', '#D9A520', '#EC8669', '#7FB2FF']
// Card fields: ink (the default), paper, indigo, cobalt, forest and coral.
const BACKGROUNDS = ['#15181F', '#F4EFE6', '#4A45D1', '#1C56AD', '#2B643B', '#EC8669']
const TEXTS = ['#FFFFFF', '#16161A', '#E8EBF2', '#4A45D1']
const DEFAULT_ACCENT = '#ADA9FF'
const DEFAULT_BG = '#15181F'
const DEFAULT_TEXT = '#FFFFFF'
const HEX = /^#[0-9a-f]{6}$/i
const okHex = (s: string) => s === '' || HEX.test(s)
const same = (a: string, b: string) => a.trim().toUpperCase() === b.toUpperCase()

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
type Colours = { accent: string; background: string; text: string }

// Redraws the sample card a moment after the branding stops changing, only while the fold is open.
function useCardPreview(repoId: string, open: boolean, logo: string, { accent, background, text }: Colours): Preview {
  const [state, setState] = useState<Preview>({ loading: false })
  const ready = (logo === '' || httpsUrl(logo)) && okHex(accent) && okHex(background) && okHex(text)
  useEffect(() => {
    if (!open || !ready) return
    let live = true
    const t = window.setTimeout(() => {
      setState((s) => ({ ...s, loading: true, error: undefined }))
      previewRepoCard(repoId, { logoUrl: logo, accent, background, text })
        .then((r) => live && setState({ png: r.png, loading: false }))
        .catch((e) => live && setState((s) => ({ ...s, loading: false, error: asError(e).message })))
    }, PREVIEW_DELAY)
    return () => {
      live = false
      window.clearTimeout(t)
    }
  }, [repoId, open, ready, logo, accent, background, text])
  return state
}

type ColourProps = {
  legend: string
  swatches: string[]
  fallback: string
  value: string
  onChange: (v: string) => void
  hint: string
}

/** One card colour: swatches plus a hex field; empty means the default. */
function ColourField({ legend, swatches, fallback, value, onChange, hint }: ColourProps) {
  const shown = HEX.test(value) ? value : fallback
  const bad = !okHex(value.trim())
  return (
    <fieldset className="rp-brand-field">
      <legend>{legend}</legend>
      <div className="rp-swatches">
        {swatches.map((c) => (
          <button
            key={c}
            type="button"
            className="rp-swatch"
            style={{ backgroundColor: c }}
            aria-label={`Use ${c} for ${legend.toLowerCase()}`}
            aria-pressed={shown.toUpperCase() === c}
            onClick={() => onChange(c)}
          />
        ))}
        <input
          className="field rp-hex"
          aria-label={`${legend} hex colour`}
          placeholder={fallback}
          maxLength={7}
          value={value}
          aria-invalid={bad || undefined}
          onChange={(e) => onChange(e.target.value)}
        />
      </div>
      <span className="rp-brand-hint">{hint}</span>
    </fieldset>
  )
}

/** The repo's card branding: a logo (the owner's GitHub avatar when empty), accent, background and text colours. */
export default function CardBrand({ repo, onSaved }: { repo: Repo; onSaved: (r: Repo) => void }) {
  const [logo, setLogo] = useState(repo.logoUrl)
  const [accent, setAccent] = useState(repo.accent)
  const [background, setBackground] = useState(repo.background)
  const [text, setText] = useState(repo.text)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [open, setOpen] = useState(false)
  const colours = { accent: accent.trim(), background: background.trim(), text: text.trim() }
  const card = useCardPreview(repo.id, open, logo.trim(), colours)

  const shown = HEX.test(accent) ? accent : DEFAULT_ACCENT
  const dirty =
    logo.trim() !== repo.logoUrl || !same(accent, repo.accent) || !same(background, repo.background) || !same(text, repo.text)
  const badHex = !okHex(colours.accent) || !okHex(colours.background) || !okHex(colours.text)
  const id = `cb-${repo.id}`
  const preview = logo.trim() || `https://github.com/${encodeURIComponent(owner(repo.fullName))}.png?size=88`

  async function save(e: FormEvent) {
    e.preventDefault()
    if (badHex || saving) return
    setSaving(true)
    setError('')
    setSaved(false)
    try {
      const next = await patchRepo(repo.id, { logoUrl: logo.trim(), ...colours })
      setLogo(next.logoUrl)
      setAccent(next.accent)
      setBackground(next.background)
      setText(next.text)
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
        <ColourField
          legend="Background"
          swatches={BACKGROUNDS}
          fallback={DEFAULT_BG}
          value={background}
          onChange={setBackground}
          hint="The card's fill. Leave empty for the ink card."
        />
        <ColourField
          legend="Text"
          swatches={TEXTS}
          fallback={DEFAULT_TEXT}
          value={text}
          onChange={setText}
          hint="Leave empty to pick white or ink, whichever reads on the background."
        />
        <ColourField
          legend="Accent"
          swatches={ACCENTS}
          fallback={DEFAULT_ACCENT}
          value={accent}
          onChange={setAccent}
          hint="Used for the avatar ring and highlights. Try any mix here; Save checks that the text and accent read on the background."
        />
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
