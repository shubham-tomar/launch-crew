import type { ReactNode } from 'react'
import './components.css'

// Outline marks on a 24-unit grid: 1.8 stroke, round caps, no fill, so they sit with the
// mark and the plane. Every icon is decorative; the control it sits in carries the name.
const MARKS: Record<string, ReactNode> = {
  refresh: (
    <>
      <path d="M20 12a8 8 0 0 1-13.9 5.4" />
      <path d="M4 12a8 8 0 0 1 13.9-5.4" />
      <path d="M18.5 3v4.2h-4.2M5.5 21v-4.2h4.2" />
    </>
  ),
  pencil: (
    <>
      <path d="M4 20h4l10.5-10.5a2.1 2.1 0 0 0-3-3L5 17v3z" />
      <path d="M13.5 8.5l2 2" />
    </>
  ),
  minus: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M8.5 12h7" />
    </>
  ),
  plus: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 8.5v7M8.5 12h7" />
    </>
  ),
  eye: (
    <>
      <path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6z" />
      <circle cx="12" cy="12" r="2.6" />
    </>
  ),
  'eye-off': (
    <>
      <path d="M4 4l16 16" />
      <path d="M10.2 6.3A10 10 0 0 1 12 6c6 0 9.5 6 9.5 6a16 16 0 0 1-3 3.5M6.6 8.2A15 15 0 0 0 2.5 12s3.5 6 9.5 6a9.6 9.6 0 0 0 3.2-.5" />
      <path d="M9.9 9.9a2.6 2.6 0 0 0 3.6 3.6" />
    </>
  ),
  copy: (
    <>
      <rect x="9" y="9" width="11" height="11" rx="2" />
      <path d="M5 15V6a2 2 0 0 1 2-2h8" />
    </>
  ),
  check: <path d="M5 12.5l4.5 4.5L19 7" />,
  external: (
    <>
      <path d="M14 4h6v6" />
      <path d="M20 4l-9 9" />
      <path d="M19 13.5V19a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h5.5" />
    </>
  ),
  qr: (
    <>
      <rect x="3.5" y="3.5" width="6.5" height="6.5" rx="1" />
      <rect x="14" y="3.5" width="6.5" height="6.5" rx="1" />
      <rect x="3.5" y="14" width="6.5" height="6.5" rx="1" />
      <path d="M14 14h2.5v2.5H14zM18 14h2.5M14 18v2.5M18 18h2.5v2.5" />
    </>
  ),
  close: <path d="M6 6l12 12M18 6L6 18" />,
  chevron: <path d="M8 10l4 4 4-4" />,
  download: <path d="M12 4v11M7.5 10.5 12 15l4.5-4.5M5 20h14" />,
  link: (
    <>
      <path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1.2 1.2" />
      <path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1.2-1.2" />
    </>
  ),
  bell: (
    <>
      <path d="M6 16V11a6 6 0 0 1 12 0v5l1.5 2h-15z" />
      <path d="M10 20a2 2 0 0 0 4 0" />
    </>
  ),
}

export type IconName = keyof typeof MARKS

type Props = { name: IconName; className?: string; spin?: boolean }

export default function Icon({ name, className, spin }: Props) {
  const cls = ['icon', spin && 'icon-spin', className].filter(Boolean).join(' ')
  return (
    <svg className={cls} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      {MARKS[name]}
    </svg>
  )
}
