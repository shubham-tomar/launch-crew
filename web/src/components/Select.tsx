import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import Icon from './Icon'
import './components.css'

export type SelectOption = { value: string; label: string }

type Props = {
  options: SelectOption[]
  value: string
  onChange: (value: string) => void
  id?: string
  disabled?: boolean
  'aria-label'?: string
  'aria-describedby'?: string
  className?: string
}

/**
 * A select-only combobox (the WAI-ARIA pattern) drawn in the app's own surface,
 * so the open list matches the theme instead of the system menu. Focus stays on
 * the button; arrows, Home, End and typed letters move, Enter or Space picks,
 * Escape and Tab close.
 */
export default function Select({ options, value, onChange, id, disabled, className, ...aria }: Props) {
  const auto = useId()
  const listId = `${id ?? auto}-list`
  const [open, setOpen] = useState(false)
  const selected = Math.max(0, options.findIndex((o) => o.value === value))
  const [active, setActive] = useState(selected)
  const root = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const typed = useRef({ text: '', at: 0 })

  useEffect(() => {
    if (!open) return
    const away = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', away)
    return () => document.removeEventListener('pointerdown', away)
  }, [open])

  useEffect(() => {
    if (open) list.current?.children[active]?.scrollIntoView?.({ block: 'nearest' })
  }, [open, active])

  function show() {
    setActive(selected)
    setOpen(true)
  }

  function pick(i: number) {
    setOpen(false)
    const o = options[i]
    if (o && o.value !== value) onChange(o.value)
  }

  // Jump to the next option starting with what was typed in the last half second.
  function seek(ch: string) {
    const now = Date.now()
    const t = typed.current
    t.text = now - t.at < 500 ? t.text + ch : ch
    t.at = now
    const from = t.text.length === 1 ? active + 1 : active
    for (let k = 0; k < options.length; k++) {
      const i = (from + k) % options.length
      if (options[i].label.toLowerCase().startsWith(t.text.toLowerCase())) {
        setActive(i)
        if (!open) pick(i)
        return
      }
    }
  }

  function onKey(e: KeyboardEvent) {
    const last = options.length - 1
    const move = (i: number) => {
      e.preventDefault()
      if (open) setActive(Math.min(last, Math.max(0, i)))
      else show()
    }
    switch (e.key) {
      case 'ArrowDown':
        return move(active + 1)
      case 'ArrowUp':
        return move(active - 1)
      case 'Home':
        return move(0)
      case 'End':
        return move(last)
      case 'Enter':
      case ' ':
        e.preventDefault()
        return open ? pick(active) : show()
      case 'Escape':
        if (open) {
          e.preventDefault()
          setOpen(false)
        }
        return
      case 'Tab':
        if (open) pick(active)
        return
      default:
        if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) seek(e.key)
    }
  }

  return (
    <div className={['select', className].filter(Boolean).join(' ')} ref={root}>
      <button
        id={id}
        type="button"
        role="combobox"
        className="field select-button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listId}
        aria-activedescendant={open ? `${listId}-${active}` : undefined}
        disabled={disabled}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={onKey}
        {...aria}
      >
        <span className="select-value">{options[selected]?.label}</span>
        <Icon name="chevron" className="select-chevron" />
      </button>
      <ul id={listId} ref={list} role="listbox" className="select-list" hidden={!open} tabIndex={-1} aria-label={aria['aria-label']}>
        {options.map((o, i) => (
          <li
            key={o.value}
            id={`${listId}-${i}`}
            role="option"
            aria-selected={i === selected}
            className={i === active ? 'select-option is-active' : 'select-option'}
            onPointerEnter={() => setActive(i)}
            onPointerDown={(e) => e.preventDefault()}
            onClick={() => pick(i)}
          >
            <span>{o.label}</span>
            {i === selected && <Icon name="check" className="select-check" />}
          </li>
        ))}
      </ul>
    </div>
  )
}
