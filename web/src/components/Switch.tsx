import './components.css'

type Props = {
  label: string
  /** What the switch belongs to; with the label it forms the accessible name ("acme/rocket: watch releases"). */
  name?: string
  checked: boolean
  disabled?: boolean
  onChange: (v: boolean) => void
}

// A labelled on/off control: the checkbox is the control, the track and thumb are its picture.
export default function Switch({ label, name, checked, disabled, onChange }: Props) {
  return (
    <label className="rp-switch">
      <input
        type="checkbox"
        role="switch"
        checked={checked}
        disabled={disabled}
        aria-label={name ? `${name}: ${label.toLowerCase()}` : label}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="rp-track" aria-hidden="true">
        <span className="rp-thumb" />
      </span>
      <span className="rp-switch-text">{label}</span>
    </label>
  )
}
