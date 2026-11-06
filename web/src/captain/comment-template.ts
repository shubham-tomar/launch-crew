// Mirrors the server's comment rendering (internal/github/comments), so the editor can
// preview as the captain types. The server is the source of truth once saved.
export const CONSENT = 'One link, your voice, your accounts. Nothing posts without your tap.'

export type Vars = { captain: string; project: string; version: string; credited: string; link: string; settings: string }

export const SAMPLE: Vars = {
  captain: 'you',
  project: 'rocket',
  version: 'v2.4.0',
  credited: 'your PR, *Split-pane resize with keyboard*',
  link: 'https://launchcrew.example/s/abc',
  settings: 'https://launchcrew.example/settings',
}

export function renderComment(template: string, defaultTemplate: string, v: Vars = SAMPLE) {
  const t = template.trim() || defaultTemplate
  let body = t
  for (const [k, val] of Object.entries(v)) body = body.split(`{${k}}`).join(val)
  if (!body.includes(CONSENT)) body += `\n\n${CONSENT}`
  if (v.settings && !body.includes(v.settings)) body += `\n\nOpt out or delete your data: ${v.settings}`
  return body
}

// What the server will refuse, said before the save. Empty means the default and is fine.
export function templateProblem(template: string, max: number) {
  const t = template.trim()
  if (!t) return ''
  if ([...t].length > max) return `Keep the comment template under ${max} characters.`
  if (!t.includes('{link}')) return 'The comment template needs {link}, or the person has nothing to tap.'
  return ''
}
