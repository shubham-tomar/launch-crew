import type { ButtonHTMLAttributes } from 'react'
import './components.css'

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary'
  busy?: boolean
}

export default function Button({ variant = 'primary', busy, className, type = 'button', ...rest }: Props) {
  const cls = ['btn', `btn-${variant}`, className].filter(Boolean).join(' ')
  return <button type={type} className={cls} aria-busy={busy || undefined} {...rest} />
}
