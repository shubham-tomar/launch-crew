import './components.css'

type Props = {
  name: string
  value: string
  label: string
  hint?: string
  checked: boolean
  disabled?: boolean
  /** radio by default; checkbox for choices that combine. */
  type?: 'radio' | 'checkbox'
  onChange: () => void
}

// A radio (or checkbox) whose whole 56 px row is the label and the target.
export default function ChoiceRow({ name, value, label, hint, checked, disabled, type = 'radio', onChange }: Props) {
  return (
    <label className="choice-row">
      <input type={type} name={name} value={value} checked={checked} disabled={disabled} onChange={onChange} />
      <span className="choice-name">{label}</span>
      {hint && <span className="choice-hint">{hint}</span>}
    </label>
  )
}
