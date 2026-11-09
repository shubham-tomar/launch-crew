const SERVICES: Record<string, string> = {
  twitter: 'X', x: 'X', bluesky: 'Bluesky', threads: 'Threads', mastodon: 'Mastodon', linkedin: 'LinkedIn',
}

export const serviceName = (s: string) => SERVICES[s.toLowerCase()] ?? s

// Images one post takes per network (Buffer's media guide, 2026-11-09); 0 means none.
const IMAGES: Record<string, number> = {
  twitter: 4, x: 4, bluesky: 4, mastodon: 4, threads: 4, linkedin: 20, facebook: 10, instagram: 20, tiktok: 10, pinterest: 1, googlebusiness: 1,
}
export const imagesPerPost = (service: string) => IMAGES[service.toLowerCase()] ?? 0

// "X", "X and Bluesky", "X, Bluesky and Mastodon".
export const listNames = (names: string[]) =>
  names.length <= 1 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`

// 12-hour clock in the viewer's timezone, without the meridiem.
export const hm = (d: Date) => `${d.getHours() % 12 || 12}:${String(d.getMinutes()).padStart(2, '0')}`
export const meridiem = (d: Date) => (d.getHours() < 12 ? 'AM' : 'PM')
export const weekday = (d: Date) => d.toLocaleDateString('en-US', { weekday: 'short' })

// "Tue 9:00"
export const when = (iso: string) => {
  const d = new Date(iso)
  return `${weekday(d)} ${hm(d)}`
}
