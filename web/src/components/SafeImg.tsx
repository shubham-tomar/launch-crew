import { useState, type ImgHTMLAttributes } from 'react'
import { IMAGE_GONE } from '../api'
import './components.css'

type Props = ImgHTMLAttributes<HTMLImageElement> & {
  /** Class for the note shown in the image's place once it fails to load. */
  goneClassName?: string
}

// An image that says so when its picture is gone: cards and uploads live in R2 under a
// 60-day rule, so a launch page can outlive its pictures.
export default function SafeImg({ goneClassName, onError, ...rest }: Props) {
  const [gone, setGone] = useState(false)
  if (gone) return <span className={['img-gone', goneClassName].filter(Boolean).join(' ')}>{IMAGE_GONE}</span>
  return (
    <img
      {...rest}
      onError={(e) => {
        setGone(true)
        onError?.(e)
      }}
    />
  )
}
