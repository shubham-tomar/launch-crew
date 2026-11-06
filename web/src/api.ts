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

export type Selection = { channelId: string; text: string; timing: Timing; assetUrl: string }
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
}
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
}

/** logoUrl and accent brand the repo's cards; empty means the owner's GitHub avatar and the default accent. */
export type Repo = { id: string; fullName: string; enabled: boolean; allowPrerelease: boolean; logoUrl: string; accent: string }

export type RepoPatch = { enabled?: boolean; allowPrerelease?: boolean; logoUrl?: string; accent?: string }

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
export const retryJob = (id: string, jobId: number) =>
  captain<{ status: string }>(`/launches/${lid(id)}/retry/${jobId}`, json('POST'))
export const retryComment = (id: string, memberId: string) =>
  captain<{ status: string }>(`/launches/${lid(id)}/crew/${encodeURIComponent(memberId)}/retry-comment`, json('POST'))
export const refreshChannels = (id: string) =>
  captain<{ channels: number }>(`/launches/${lid(id)}/channels/refresh`, json('POST'))
export const listRepos = () => captain<Repo[]>('/repos')

/** The captain's templates: the PR comment wording and notes for the model. Empty means the default. */
export type CaptainSettings = {
  commentTemplate: string
  draftNotes: string
  defaultComment: string
  placeholders: string[]
  preview: string
  maxCommentLen: number
  maxNotesLen: number
}
export const getSettings = () => captain<CaptainSettings>('/settings')
export const saveSettings = (p: { commentTemplate?: string; draftNotes?: string }) => captain<CaptainSettings>('/settings', json('PUT', p))

// Where a post lives in the Buffer app: the channel's queue, drafts or sent list.
export const bufferChannelUrl = (channelId: string, status: string) => {
  const tab = status === 'posted' ? 'sent' : status === 'draft' ? 'drafts' : 'queue'
  return `https://publish.buffer.com/channels/${encodeURIComponent(channelId)}/${tab}`
}
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
}

export const getMe = () => call<Me>('/api/me')
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
