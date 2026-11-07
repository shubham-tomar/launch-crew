import QRCode from 'qrcode'
import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import { ApiError, showInfo, type ShowInfo } from '../api'
import Icon from '../components/Icon'
import Wordmark from '../components/Wordmark'
import { Dead } from './shared'
import '../styles/print.css'
import './crew.css'

const SIDE = 800
// The centre tile covers a fifth of the QR's width; error correction H absorbs it.
const TILE = 0.2
const PLANE_PATH =
  'M12 2.2c1.05 0 1.85 1.3 1.85 3v4.1l7 3.9v2.1l-7 -2v3.5l2.35 1.85v1.55L12 19.1l-4.2 1.2v-1.55l2.35 -1.85v-3.5l-7 2v-2.1l7 -3.9V5.2c0 -1.7 0.8 -3 1.85 -3z'

// Canvas cannot read CSS variables, so the slide's colours are read from the tokens at
// draw time. The fallbacks are DESIGN.md's qr-slide values, for a canvas drawn before
// the stylesheet has applied.
const FALLBACK: Record<string, string> = { '--color-card-ink': '#15181f', '--color-slide-text': '#f2f3f7', '--color-primary': '#4a45d1' }
function token(name: keyof typeof FALLBACK) {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || FALLBACK[name]
}

export type Logo = { kind: 'plane' } | { kind: 'none' } | { kind: 'image'; url: string }

// What goes in the middle of the QR: the plane unless the captain chose otherwise.
export function logoOf(view: Pick<ShowInfo, 'qrLogo' | 'qrLogoUrl'>): Logo {
  if (view.qrLogo === 'none') return { kind: 'none' }
  if (view.qrLogo === 'custom' && view.qrLogoUrl) return { kind: 'image', url: view.qrLogoUrl }
  return { kind: 'plane' }
}

function roundTile(ctx: CanvasRenderingContext2D, x: number, y: number, size: number, r: number) {
  ctx.beginPath()
  ctx.moveTo(x + r, y)
  ctx.arcTo(x + size, y, x + size, y + size, r)
  ctx.arcTo(x + size, y + size, x, y + size, r)
  ctx.arcTo(x, y + size, x, y, r)
  ctx.arcTo(x, y, x + size, y, r)
  ctx.closePath()
}

function drawPlane(ctx: CanvasRenderingContext2D, mid: number, size: number) {
  ctx.save()
  ctx.translate(mid, mid)
  ctx.scale(size / 24, size / 24)
  ctx.rotate(Math.PI / 4)
  ctx.translate(-12, -12)
  ctx.fillStyle = token('--color-primary')
  ctx.fill(new Path2D(PLANE_PATH))
  ctx.restore()
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.crossOrigin = 'anonymous'
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error('logo did not load'))
    img.src = url
  })
}

// QR with error correction H and a light tile with the logo over the centre. Modules are
// card-ink on slide-text: both tokens are pinned across schemes, so the code stays scannable.
async function paint(canvas: HTMLCanvasElement, text: string, logo: Logo) {
  const tile = token('--color-slide-text')
  await QRCode.toCanvas(canvas, text, {
    errorCorrectionLevel: 'H',
    width: SIDE,
    margin: 2,
    color: { dark: token('--color-card-ink'), light: tile },
  })
  // qrcode writes inline px sizes that beat the stylesheet; let CSS size the slide.
  canvas.style.width = ''
  canvas.style.height = ''
  const ctx = canvas.getContext('2d')
  if (!ctx || logo.kind === 'none') return
  const size = canvas.width * TILE
  const mid = canvas.width / 2
  ctx.fillStyle = tile
  roundTile(ctx, mid - size / 2, mid - size / 2, size, size * 0.22)
  ctx.fill()
  if (logo.kind === 'image') {
    try {
      const img = await loadImage(logo.url)
      const box = size * 0.76
      const k = Math.min(box / img.width, box / img.height)
      ctx.drawImage(img, mid - (img.width * k) / 2, mid - (img.height * k) / 2, img.width * k, img.height * k)
      return
    } catch {
      /* fall back to the plane */
    }
  }
  drawPlane(ctx, mid, size * 0.7)
}

// Only a captain launch page is a place to go back to; anything else in ?back= is ignored.
export function safeBack(v: string | null): string | null {
  return v && /^\/captain\/launches\/[\w-]+$/.test(v) ? v : null
}

export default function Show() {
  const openToken = useParams().openToken ?? ''
  const navigate = useNavigate()
  const location = useLocation()
  const [searchParams] = useSearchParams()
  const [view, setView] = useState<ShowInfo>()
  const [error, setError] = useState<ApiError>()
  const [failed, setFailed] = useState(false)
  const canvas = useRef<HTMLCanvasElement>(null)

  // The slide fills the screen, so it needs its own way out: the launch page named in
  // ?back= (the captain's button opens the slide with a full page load, so there is no
  // in-app history), else the page that opened it, else the captain's launches. Escape
  // does the same.
  const back = safeBack(searchParams.get('back'))
  const close = () => {
    if (back) navigate(back)
    else if (location.key !== 'default') navigate(-1)
    else navigate('/captain')
  }

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close()
    }
    document.addEventListener('keydown', key)
    return () => document.removeEventListener('keydown', key)
    // close only reads back, location.key and navigate, which change with the route
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.key, back])

  useEffect(() => {
    let live = true
    showInfo(openToken)
      .then((v) => live && setView(v))
      .catch((e) => live && setError(e instanceof ApiError ? e : new ApiError(0, 'error', String(e))))
    return () => {
      live = false
    }
  }, [openToken])

  useEffect(() => {
    if (view && canvas.current) paint(canvas.current, view.shortUrl, logoOf(view)).catch(() => setFailed(true))
  }, [view])

  if (error) return <Dead code={error.code} />

  return (
    <main className="show">
      <button type="button" className="show-close" aria-label="Close" title="Close (Esc)" onClick={close}>
        <Icon name="close" />
      </button>
      {view && (
        <>
          <h1 className="show-title">{view.title}</h1>
          <canvas
            ref={canvas}
            className="show-qr"
            width={SIDE}
            height={SIDE}
            role="img"
            aria-label={`QR code for ${view.shortUrl}`}
          />
          {failed && <p className="crew-note">The QR code could not be drawn. Use the link below.</p>}
          <p className="show-link">{view.shortUrl}</p>
        </>
      )}
      <footer className="show-foot">
        <Wordmark />
        <a className="show-download" href={`/api/l/${encodeURIComponent(openToken)}/show.png`} download>
          Download PNG
        </a>
      </footer>
    </main>
  )
}
