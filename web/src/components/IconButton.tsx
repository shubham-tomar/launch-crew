import type { ButtonHTMLAttributes } from 'react'
import Icon, { type IconName } from './Icon'
import './components.css'

type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> & {
  /** The icon drawn in the button. */
  icon: IconName
  /** What the button does, read by screen readers and shown as a tooltip. */
  label: string
  /** Spin the icon while the action runs; the button stays focusable but inert. */
  busy?: boolean
  /** Ink the icon in the accent, e.g. once a copy succeeded. */
  tone?: 'default' | 'accent'
}

// A 44px square secondary button that carries only an icon. The label is the accessible
// name and the tooltip, so the control still reads as words to anyone who needs them.
export default function IconButton({ icon, label, busy, tone = 'default', className, type = 'button', disabled, ...rest }: Props) {
  const cls = ['btn', 'btn-secondary', 'btn-icon', tone === 'accent' && 'btn-icon-accent', className].filter(Boolean).join(' ')
  return (
    <button
      type={type}
      className={cls}
      aria-label={label}
      title={label}
      aria-busy={busy || undefined}
      aria-disabled={busy || undefined}
      disabled={disabled}
      {...rest}
    >
      <Icon name={icon} spin={busy && icon === 'refresh'} />
    </button>
  )
}
