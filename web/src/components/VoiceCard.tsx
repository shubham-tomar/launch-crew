import { useId, useRef, useState } from 'react'
import type { KeyboardEvent } from 'react'
import { ApiError, type VoiceProfile } from '../api'
import Alert from './Alert'
import Button from './Button'
import './components.css'

export type VoiceEdit = Pick<VoiceProfile, 'sentenceLength' | 'case' | 'hashtags' | 'extra'>

type Props = {
  profile: Partial<VoiceProfile>
  onSave: (edit: VoiceEdit) => Promise<void>
  onClose: () => void
}

const GROUPS = [
  { key: 'sentenceLength', legend: 'Sentence length', options: [['short', 'short'], ['medium', 'medium'], ['long', 'long']] },
  { key: 'case', legend: 'Case', options: [['lowercase', 'lowercase'], ['sentence', 'sentence case']] },
  { key: 'hashtags', legend: 'Hashtags', options: [['none', 'none'], ['few', 'one or two']] },
] as const

const BUSY = 'Buffer is not answering, try again'

// Editable voice: three radio groups and a short note. Saving rewrites the drafts.
export default function VoiceCard({ profile, onSave, onClose }: Props) {
  const name = useId()
  const [edit, setEdit] = useState<VoiceEdit>({
    sentenceLength: profile.sentenceLength || 'medium',
    case: profile.case || 'sentence',
    hashtags: profile.hashtags || 'none',
    extra: profile.extra ?? '',
  })
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')
  const inflight = useRef(false)

  async function save() {
    if (inflight.current) return
    inflight.current = true
    setBusy(true)
    setError('')
    setSaved(false)
    try {
      await onSave(edit)
      setSaved(true)
    } catch (e) {
      setError(e instanceof ApiError && e.status !== 502 ? e.message : BUSY)
    } finally {
      inflight.current = false
      setBusy(false)
    }
  }

  function key(e: KeyboardEvent) {
    if (e.key !== 'Escape') return
    e.stopPropagation()
    onClose()
  }

  return (
    <div role="region" aria-label="Voice" className="voicecard" onKeyDown={key}>
      {GROUPS.map((g) => (
        <fieldset key={g.key} className="crew-fieldset voicecard-group">
          <legend>{g.legend}</legend>
          <div className="crew-chips">
            {g.options.map(([value, label]) => (
              <label key={value} className="crew-radio">
                <input
                  type="radio"
                  name={`${name}-${g.key}`}
                  checked={edit[g.key] === value}
                  onChange={() => setEdit((v) => ({ ...v, [g.key]: value }))}
                />
                {label}
              </label>
            ))}
          </div>
        </fieldset>
      ))}
      <label className="voicecard-extra">
        <span className="crew-note">Anything else</span>
        <input
          type="text"
          className="voicecard-input"
          maxLength={80}
          placeholder="e.g. never say excited"
          value={edit.extra}
          onChange={(e) => setEdit((v) => ({ ...v, extra: e.target.value }))}
        />
      </label>
      {error && <Alert tone="error">{error}</Alert>}
      {saved && <Alert tone="success" role="status">Saved. Your drafts are being rewritten.</Alert>}
      <div className="voicecard-actions">
        <Button variant="secondary" busy={busy} onClick={save}>Rewrite in this voice</Button>
        <Button variant="secondary" onClick={onClose}>Done</Button>
      </div>
    </div>
  )
}
