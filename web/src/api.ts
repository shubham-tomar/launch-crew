export type View = {
  launch: {
    id: string
    title: string
    summary: string
    project: string
    version: string
    releaseUrl: string
    status: string
    launchAt: string | null
    windowMinutes: number
    source: string
    shortUrl: string
  }
  member: { id: string; name: string; role: string; roleHint: string; state: string; verified: boolean }
  card: { url: string; alt: string } | null
  preview: string
  verified: boolean
  foreign: boolean
  needsVerify: boolean
  connected: boolean
  copyText: string
  /** The captain's uploads every post carries, in order, after the card when includeCard is on.
   * bluesky is what a Bluesky post gets: the original under 1 MB, a smaller copy, or '' for nothing. */
  images: { url: string; bluesky: string }[]
  includeCard: boolean
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

export type Channel = {
  id: string
  name: string
  service: string
  avatar: string
  eligible: boolean
  reason: string
  checked: boolean
}

export type DraftView = {
  id: string
  channelId: string
  network: string
  text: string
  thread: string[]
  firstComment: string
  fromModel: boolean
  edited: boolean
  flagged: boolean
  status: string
  bufferPostId: string
}

export type Timing = 'window' | 'queue' | 'draft'

export type VoiceProfile = {
  sentenceLength: string
  case: string
  hashtags: string
  emoji: string
  hook: string
  signOff: string
  link: string
  extra: string
}

export type ApproveView = {
  memberId: string
  channels: Channel[]
  drafts: DraftView[]
  voice: Partial<VoiceProfile>
  voiceLine: string
  timing: Timing[]
  closed: boolean
  needsReconnect: boolean
  voicedReady: boolean
}

/** One channel to post to. `attach` asks for the launch's images; the server picks which files when it posts. */
export type Selection = { channelId: string; text: string; timing: Timing; attach: boolean }
export type Outcome = { channelId: string; service: string; status: 'queued' | 'draft' | 'failed'; when: string | null; note: string; postId?: string }

export const OFFLINE = 'Could not reach Launch Crew. Check your connection and try again.'

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, init)
  } catch {
    // The browser's own text ("Failed to fetch") names no next step; this does.
    throw new ApiError(0, 'offline', OFFLINE)
  }
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, body.error ?? 'error', body.message ?? res.statusText)
  return body as T
}

export function getInvite(token: string, opts: { foreign?: boolean } = {}) {
  const q = opts.foreign ? '?foreign=1' : ''
  return call<View>(`/api/s/${encodeURIComponent(token)}${q}`)
}

export function share(token: string) {
  return call<{ text: string }>(`/api/s/${encodeURIComponent(token)}/share`, { method: 'POST' })
}

function crew<T>(id: string, token: string, path: string, method: string, body?: unknown) {
  const headers: Record<string, string> = { 'X-Crew-Token': token }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  return call<T>(`/api/crew/${encodeURIComponent(id)}${path}`, {
    method,
    headers,
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export const connect = (id: string, token: string) => crew<ApproveView>(id, token, '/connect', 'POST')

export const refreshMyChannels = (id: string, token: string) => crew<ApproveView>(id, token, '/channels/refresh', 'POST')

export const getDrafts = (id: string, token: string) =>
  crew<{ drafts: DraftView[]; voicedReady: boolean }>(id, token, '/drafts', 'GET')

export const saveDraft = (id: string, token: string, draftId: string, text: string) =>
  crew<unknown>(id, token, `/drafts/${encodeURIComponent(draftId)}`, 'PUT', { text })

export const post = (id: string, token: string, selections: Selection[]) =>
  crew<{ outcomes: Outcome[] }>(id, token, '/post', 'POST', { selections })

export const disconnect = (id: string, token: string) => crew<unknown>(id, token, '/disconnect', 'POST')

export const updateVoice = (id: string, token: string, profile: Partial<VoiceProfile>) =>
  crew<{ voiceLine: string }>(id, token, '/voice', 'PUT', profile)

export type BoardRow = { name: string; role: string; state: string; when: string | null }
export type BoardView = { rows: BoardRow[]; in: number; total: number }

export const board = (id: string, token: string) => crew<BoardView>(id, token, '/board', 'GET')

export const remember = (id: string, token: string, value: boolean) =>
  crew<unknown>(id, token, '/remember', 'POST', { remember: value })

export type Role = { name: string; hint: string }
export type LaunchView = {
  title: string
  summary: string
  roles: Role[]
  card: { url: string; alt: string } | null
  shortUrl: string
  openCount: number
}

export const openLink = (openToken: string) => call<LaunchView>(`/api/l/${encodeURIComponent(openToken)}`)

export type ShowInfo = {
  title: string
  shortUrl: string
  /** Centre mark of the QR: the plane (default), nothing, or the captain's own image. */
  qrLogo?: 'plane' | 'none' | 'custom'
  qrLogoUrl?: string
}
export const showInfo = (openToken: string) => call<ShowInfo>(`/api/l/${encodeURIComponent(openToken)}/show`)

export const chooseRole = (
  openToken: string,
  body: { role: string; hint: string; talkTitle: string; other: boolean },
) =>
  call<{ token: string; memberId: string }>(`/api/l/${encodeURIComponent(openToken)}/role`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })

// Captain API
export type LaunchSummary = {
  id: string
  status: 'draft' | 'launched' | 'closed'
  source: 'github' | 'manual'
  title: string
  releaseTag: string
  launchAt: string | null
  windowMinutes: number
  openCount: number
  createdAt: string
  /** Full repo name for a GitHub launch, '' for a manual one. */
  repo: string
  /** Headline PRs in the launch (0 for a manual launch). */
  prCount: number
  /** People in the crew who are in (approved, queued or posted) and the crew size, captain not counted. */
  crewIn: number
  crewTotal: number
  /** Initials of up to 5 crew members, for the avatar stack. */
  crewInitials: string[]
  /** The captain's own name: their GitHub login, else the repo owner, else ''. */
  captainName: string
}

export type Pr = { prNumber: number; title: string; authorLogin: string; hidden: boolean }
export type CaptainDraft = {
  id: string
  channelId: string
  channelName: string
  service: string
  text: string
  status: 'draft' | 'sending' | 'queued' | 'posted' | 'failed' | 'skipped'
  bufferPostId: string
  note: string
  /** The post's URL on the network once it was sent. */
  externalLink?: string
}
/** One post a crew member has in their Buffer for this launch. */
export type CrewPost = { network: string; status: string; externalLink: string }
export type CrewRow = {
  id: string
  name: string
  login: string
  role: string
  state: string
  commentFailed: boolean
  commentRetriesLeft: number
  inviteUrl?: string
  /** Their contributor card, once the launch drew it. */
  cardUrl?: string
  posts?: CrewPost[]
}
export type Check = { id: number; name: string; ok: boolean; fix: string }
export type DeadLetter = { jobId: number; kind: string; lastError: string; attempts: number }

export type Detail = {
  id: string
  status: 'draft' | 'launched' | 'closed'
  source: 'github' | 'manual'
  title: string
  summary: string
  releaseTag: string
  releaseUrl: string
  launchAt: string | null
  windowMinutes: number
  crewCap: number
  openCount: number
  openUrl: string
  showUrl: string
  pngUrl: string
  roles: Role[]
  bufferConnected: boolean
  headlines: Pr[]
  chores: Pr[]
  captainDrafts: CaptainDraft[]
  crew: CrewRow[]
  checks: Check[]
  deadLetters: DeadLetter[]
  counts: { in: number; total: number }
  /** The captain's own name, shown as the first member of the crew. */
  captainName: string
  /** QR centre mark on the show slide and PNG: the plane, nothing, or the captain's own image. */
  qrLogo: QrLogo
  qrLogoUrl: string
  /** The release card, attached to the captain's posts when there is no hero image. */
  cardUrl?: string
  /** The captain's uploads, in posting order. */
  images: LaunchImage[]
  /** Whether posts lead with the card slot (hero, else release card) before the images. */
  includeCard: boolean
}

export type LaunchImage = { id: string; url: string }

export const MAX_IMAGES = 10
export const MAX_IMAGE_BYTES = 10 << 20
export const IMAGE_TYPES = ['image/jpeg', 'image/png', 'image/gif', 'image/webp']
export const IMAGE_TOO_LARGE = 'Each image can be up to 10 MB.'
export const IMAGE_NOT_IMAGE = 'Use a JPEG, PNG, GIF or WebP image.'
/** What a tile says when its picture no longer loads: R2 drops uploads after 60 days. */
export const IMAGE_GONE = 'Removed after 60 days'
export type QrLogo = 'plane' | 'none' | 'custom'

export type NewLaunch = {
  title: string
  summary?: string
  roles?: Role[]
  heroUrl?: string
  launchAt?: string
  windowMinutes?: number
  qrLogo?: QrLogo
  /** https image URL, only with qrLogo 'custom'. */
  qrLogoUrl?: string
  /** One of the captain's templates; absent for the built-in default. */
  templateId?: string
}

export type Patch = {
  title?: string
  summary?: string
  launchAt?: string
  windowMinutes?: number
  crewCap?: number
  hiddenPrs?: number[]
  qrLogo?: QrLogo
  qrLogoUrl?: string
  tickedCrew?: string[]
  includeCard?: boolean
}

/** logoUrl and the colours brand the repo's cards; empty means the owner's GitHub avatar and the default ink card. */
export type Repo = {
  id: string
  fullName: string
  enabled: boolean
  allowPrerelease: boolean
  logoUrl: string
  accent: string
  background: string
  text: string
  /** The template its release launches use; '' for the built-in default. */
  templateId: string
}

export type RepoPatch = {
  enabled?: boolean
  allowPrerelease?: boolean
  logoUrl?: string
  accent?: string
  background?: string
  text?: string
  templateId?: string
}

const json = (method: string, body?: unknown): RequestInit => ({
  method,
  headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
  body: body === undefined ? undefined : JSON.stringify(body),
})

// A signed-out captain goes through the Buffer sign-in and comes back here.
export function signIn() {
  const back = location.pathname + location.search
  location.assign(`/auth/buffer/start?return_to=${encodeURIComponent(back)}`)
}

async function captain<T>(path: string, init?: RequestInit): Promise<T> {
  try {
    return await call<T>(`/api/captain${path}`, init)
  } catch (e) {
    if (e instanceof ApiError && e.status === 401 && e.code === 'sign_in') signIn()
    throw e
  }
}

const lid = encodeURIComponent

export const listLaunches = () => captain<LaunchSummary[]>('/launches')
export const createLaunch = (b: NewLaunch) => captain<Detail>('/launches', json('POST', b))
export const getLaunch = (id: string) => captain<Detail>(`/launches/${lid(id)}`)
export const patchLaunch = (id: string, p: Patch) => captain<Detail>(`/launches/${lid(id)}`, json('PATCH', p))
// text edits the post; skipped removes it from (true) or restores it to (false) the launch.
export const patchCaptainDraft = (id: string, draftId: string, body: { text?: string; skipped?: boolean }) =>
  captain<Detail>(`/launches/${lid(id)}/drafts/${encodeURIComponent(draftId)}`, json('PATCH', body))
export const editCaptainDraft = (id: string, draftId: string, text: string) => patchCaptainDraft(id, draftId, { text })
export const pressLaunch = (id: string) => captain<{ status: string }>(`/launches/${lid(id)}/launch`, json('POST'))
// Uploads go over XMLHttpRequest so the tile can show how far along the bytes are.
export function uploadLaunchImage(id: string, file: File, onProgress?: (fraction: number) => void): Promise<LaunchImage> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/captain/launches/${lid(id)}/images`)
    xhr.responseType = 'text'
    if (onProgress) {
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && e.total > 0) onProgress(e.loaded / e.total)
      }
    }
    xhr.onerror = () => reject(new ApiError(0, 'offline', OFFLINE))
    xhr.onload = () => {
      let body: { id?: string; url?: string; error?: string; message?: string } = {}
      try {
        body = JSON.parse(xhr.responseText || '{}')
      } catch {
        body = {}
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve({ id: body.id ?? '', url: body.url ?? '' })
        return
      }
      const err = new ApiError(xhr.status, body.error ?? 'error', body.message ?? xhr.statusText)
      if (err.status === 401 && err.code === 'sign_in') signIn()
      reject(err)
    }
    const form = new FormData()
    form.append('image', file, file.name)
    xhr.send(form)
  })
}
export const removeLaunchImage = (id: string, imageId: string) =>
  captain<unknown>(`/launches/${lid(id)}/images/${lid(imageId)}`, json('DELETE'))
export const orderLaunchImages = (id: string, ids: string[]) =>
  captain<unknown>(`/launches/${lid(id)}/images/order`, json('PUT', { ids }))
export const retryJob = (id: string, jobId: number) =>
  captain<{ status: string }>(`/launches/${lid(id)}/retry/${jobId}`, json('POST'))
export const retryComment = (id: string, memberId: string) =>
  captain<{ status: string }>(`/launches/${lid(id)}/crew/${encodeURIComponent(memberId)}/retry-comment`, json('POST'))
export const refreshChannels = (id: string) =>
  captain<{ channels: number }>(`/launches/${lid(id)}/channels/refresh`, json('POST'))
export const listRepos = () => captain<Repo[]>('/repos')

/** One named template: the PR comment wording and notes for the model. Empty fields mean the built-in comment and no notes. */
export type Template = { id: string; name: string; commentTemplate: string; draftNotes: string; preview: string }
export type TemplateInput = { name: string; commentTemplate: string; draftNotes: string }
/** The captain's templates and the editor's limits. A launch or repo with no template uses the built-in comment. */
export type TemplatesView = {
  templates: Template[]
  defaultComment: string
  defaultPreview: string
  placeholders: string[]
  maxCommentLen: number
  maxNotesLen: number
  maxNameLen: number
  maxTemplates: number
}
export const listTemplates = () => captain<TemplatesView>('/templates')
export const createTemplate = (t: TemplateInput) => captain<Template>('/templates', json('POST', t))
export const saveTemplate = (id: string, t: TemplateInput) => captain<Template>(`/templates/${lid(id)}`, json('PUT', t))
export const deleteTemplate = (id: string) => captain<unknown>(`/templates/${lid(id)}`, json('DELETE'))
/** Draws the event slide for the new launch form; png is a data URL. */
export const previewQr = (p: { title: string; qrLogo: QrLogo; qrLogoUrl?: string }) =>
  captain<{ png: string }>('/qr-preview', json('POST', p))

// Where a post lives in the Buffer app: the channel's queue, drafts or sent list.
export const bufferChannelUrl = (channelId: string, status: string) => {
  const tab = status === 'posted' ? 'sent' : status === 'draft' ? 'drafts' : 'queue'
  return `https://publish.buffer.com/channels/${encodeURIComponent(channelId)}/${tab}`
}
/** Draws a sample card with unsaved branding; png is a data URL. */
export const previewRepoCard = (id: string, p: { logoUrl: string; accent: string; background: string; text: string }) =>
  captain<{ png: string }>(`/repos/${lid(id)}/card-preview`, json('POST', p))
export const patchRepo = (id: string, p: RepoPatch) =>
  captain<Repo>(`/repos/${lid(id)}`, json('PATCH', p))

export type Me = {
  signedIn: boolean
  githubLogins: string[]
  bufferConnected: boolean
  optedOut: boolean
  launches: number
  crewRows: number
  supportEmail: string
  /** How a captain hears that a release made a draft; absent for crew only. */
  notify?: Notify
}

/** Email is on only for a verified address; emailPending awaits its link. */
export type Notify = {
  github: boolean
  releaseNotes: boolean
  email: boolean
  emailAddress: string
  emailPending: string
  mailAvailable: boolean
}

export const getMe = () => call<Me>('/api/me')
export const setNotify = (p: { github: boolean; releaseNotes: boolean; email: boolean }) =>
  call<Notify>('/api/me/notify', json('PUT', p))
export const startNotifyEmail = (address: string) => call<Notify>('/api/me/notify/email', json('POST', { address }))
export const clearNotifyEmail = () => call<Notify>('/api/me/notify/email', json('DELETE'))
// scope says how far the opt-out reached: every future launch ('all') or just
// the launch the token belongs to ('launch').
export type OptOut = { optedOut: boolean; scope: 'all' | 'launch' }
export const optOutMe = () => call<OptOut>('/api/me/opt-out', json('POST'))
export const optInMe = () => call<{ optedOut: boolean }>('/api/me/opt-out', json('DELETE'))
// The PR comment footer link carries the invite token, so this works signed out.
// On its own it opts out of that launch; the server widens it to every launch
// only when the session has proved the token's GitHub account.
export const optOutWithToken = (token: string) =>
  call<OptOut>(`/api/opt-out?token=${encodeURIComponent(token)}`, json('POST'))
export const deleteMe = () => call<{ deleted: boolean }>('/api/me', json('DELETE'))
