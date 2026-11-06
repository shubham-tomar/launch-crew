import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router'
import { share } from '../api'
import Button from '../components/Button'
import Card from '../components/Card'
import Skeleton from '../components/Skeleton'
import { CrewFrame, Dead, Foreign, useInvite } from './shared'

export default function Fallback() {
  const token = useParams().token ?? ''
  const { view, error } = useInvite(token)
  const [text, setText] = useState<string>()
  const [copied, setCopied] = useState(false)
  const [manual, setManual] = useState(false)
  const [announce, setAnnounce] = useState('')
  const area = useRef<HTMLTextAreaElement>(null)
  const timer = useRef<number>(undefined)

  const ok = view !== undefined && !view.foreign

  // Opening the page marks the row copied server-side; the reply is the canonical text.
  // Only for the right person: a foreign link must not touch the row.
  useEffect(() => {
    if (!ok) return
    share(token).then((r) => setText(r.text), () => {})
  }, [token, ok])

  useEffect(() => () => window.clearTimeout(timer.current), [])

  useEffect(() => {
    if (!manual) return
    area.current?.focus()
    area.current?.select()
  }, [manual])

  if (error) return <Dead code={error.code} />
  if (view?.foreign) return <Foreign releaseUrl={view.launch.releaseUrl} />
  const draft = text ?? view?.copyText
  const q = encodeURIComponent

  async function copy() {
    if (draft === undefined) return
    try {
      await navigator.clipboard.writeText(draft)
      setCopied(true)
      setAnnounce('Copied')
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => {
        setCopied(false)
        setAnnounce('')
      }, 2000)
    } catch {
      setManual(true)
      setAnnounce('Select and copy')
    }
  }

  return (
    <CrewFrame version={view?.launch.version} footer>
      <div className="visually-hidden" role="status">
        {announce}
      </div>
      <div className="crew-stack" aria-busy={!view || draft === undefined || undefined}>
        {view ? (
          <Card project={view.launch.project} version={view.launch.version} title={view.launch.title} image={view.card} />
        ) : (
          <Skeleton lines={4} />
        )}
        <div className="perf perf-h crew-perf" aria-hidden="true" />
        {draft === undefined ? <Skeleton lines={3} /> : <p className="crew-preview">{draft}</p>}
        {draft !== undefined && (
          <div className="crew-actions">
            <Button onClick={copy}>{copied ? 'Copied' : 'Copy text'}</Button>
            {manual && (
              <>
                <textarea ref={area} className="crew-draft" readOnly value={draft} aria-label="Draft text" />
                <p className="crew-note">Select and copy</p>
              </>
            )}
            {view?.card && (
              <a className="btn btn-secondary" href={view.card.url} download>
                Download card
              </a>
            )}
            <a className="btn btn-secondary" rel="noopener noreferrer" href={`https://twitter.com/intent/tweet?text=${q(draft)}`}>
              Open in X
            </a>
            <a className="btn btn-secondary" rel="noopener noreferrer" href={`https://bsky.app/intent/compose?text=${q(draft)}`}>
              Open in Bluesky
            </a>
            <p className="crew-note">LinkedIn doesn't accept pre-filled text — copy above, then post.</p>
            <a className="btn btn-secondary" href={`/auth/buffer/start?signup=1&return_to=/s/${q(token)}/approve`}>
              Create free Buffer account
            </a>
            <Link className="btn btn-secondary" to={`/s/${q(token)}`}>
              Back
            </Link>
          </div>
        )}
      </div>
    </CrewFrame>
  )
}
