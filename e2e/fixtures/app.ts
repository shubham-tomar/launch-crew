// Helpers shared by the specs: the /e2e control routes and the sign-in
// round trips (every OAuth page is a fake that approves at once).
import { expect, type APIRequestContext, type Browser, type Page } from '@playwright/test'

export type Post = { id: string; channelId: string; mode: string; dueAt?: string; saveToDraft: boolean; text: string }
export type LaunchRow = { id: string; status: string; title: string; launchAt: string | null; windowMinutes: number }
export type Comment = { id: number; body: string }

async function ok<T>(res: Promise<import('@playwright/test').APIResponse>): Promise<T> {
  const r = await res
  if (!r.ok()) throw new Error(`${r.url()}: ${r.status()} ${await r.text()}`)
  return (await r.json()) as T
}

export const control = {
  reset: (rq: APIRequestContext) => ok(rq.post('/e2e/reset')),
  release: (rq: APIRequestContext) => ok<{ status: number }>(rq.post('/e2e/release')),
  posts: (rq: APIRequestContext) => ok<Post[]>(rq.get('/e2e/buffer/posts')),
  launches: (rq: APIRequestContext) => ok<LaunchRow[]>(rq.get('/e2e/launches')),
  comments: (rq: APIRequestContext, pr: number) => ok<Comment[]>(rq.get(`/e2e/github/comments/${pr}`)),
  bufferAccount: (rq: APIRequestContext, id: string) => ok(rq.post('/e2e/buffer/account', { data: { id } })),
  githubUser: (rq: APIRequestContext, id: number, login: string) => ok(rq.post('/e2e/github/user', { data: { id, login } })),
  r2Missing: (rq: APIRequestContext, missing: boolean) => ok(rq.post('/e2e/r2/missing', { data: { missing } })),
  closeWindow: (rq: APIRequestContext, launchId: string) => ok<{ rows: number }>(rq.post('/e2e/close-window', { data: { launchId } })),
  deadLetter: (rq: APIRequestContext, launchId: string) => ok(rq.post('/e2e/dead-letter', { data: { launchId } })),
  expireInvites: (rq: APIRequestContext) => ok(rq.post('/e2e/expire-invites')),
}

export const people = {
  alice: { id: 91001, login: 'alice', pr: 11 },
  bob: { id: 91002, login: 'bob', pr: 12 },
  captain: { id: 910010, login: 'captain' },
}

/** Polls fn until it returns a truthy value. */
export async function until<T>(fn: () => Promise<T>, what: string, timeout = 20_000): Promise<NonNullable<T>> {
  const end = Date.now() + timeout
  for (;;) {
    const v = await fn()
    if (v) return v as NonNullable<T>
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`)
    await new Promise((r) => setTimeout(r, 250))
  }
}

/** A datetime-local value minutes from now, in the browser's (and our) zone. */
export function localInput(minutesFromNow: number): string {
  const d = new Date(Date.now() + minutesFromNow * 60_000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

/** Captain signs in with Buffer (account "captain"). */
export async function signInCaptain(page: Page, rq: APIRequestContext) {
  await control.bufferAccount(rq, 'captain')
  await page.goto('/auth/buffer/start?return_to=/captain')
  await expect(page).toHaveURL(/\/captain$/)
}

/** Installs the GitHub App as the captain; lands on the repos page. */
export async function installApp(page: Page, rq: APIRequestContext) {
  await control.githubUser(rq, people.captain.id, people.captain.login)
  await page.goto('/github/install/start')
  await expect(page).toHaveURL(/\/captain\/repos$/)
  await expect(page.getByText('acme/rocket')).toBeVisible()
}

/** Sends the release webhook and waits for the worker to build the draft launch. */
export async function releaseLaunch(rq: APIRequestContext): Promise<string> {
  expect((await control.release(rq)).status).toBe(202)
  const row = await until(async () => (await control.launches(rq)).find((l) => l.title === 'Rocket 1.2'), 'release launch')
  return row.id
}

/** Sets the launch moment on the captain detail page. */
export async function setLaunchTime(page: Page, minutesFromNow: number) {
  // The editor opens by itself while no time is set; otherwise it sits behind "Change".
  await expect(page.getByRole('heading', { name: 'Timing' })).toBeVisible()
  const field = page.getByLabel('Launch moment')
  if (!(await field.isVisible())) await page.getByRole('button', { name: 'Change moment or window' }).click()
  await field.fill(localInput(minutesFromNow))
  // Timing saves itself once the time is valid.
  await expect(page.getByRole('status').filter({ hasText: /^Saved$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Launch( ·.*)?$/ })).toBeVisible()
}

/** The launch button, in any of its labels. */
export const launchButton = (page: Page) => page.getByRole('button', { name: /^(Launch|Launched)( ·.*)?$/ })

/** Captain path to a launched GitHub launch; returns its id. */
export async function launchGitHub(page: Page, rq: APIRequestContext): Promise<string> {
  await signInCaptain(page, rq)
  await installApp(page, rq)
  const id = await releaseLaunch(rq)
  await page.goto(`/captain/launches/${id}`)
  await setLaunchTime(page, 60)
  await expect(launchButton(page)).toHaveText('Launch')
  await launchButton(page).click()
  await expect(launchButton(page)).toHaveText('Launched')
  return id
}

const inviteRe = /https?:\/\/[^/\s)]+(\/s\/[A-Za-z0-9_-]+)/

/** Waits for the invite comment on the PR and returns its path (/s/<token>). The
 * captain's "draft is ready" mention may share the PR; it carries no invite. */
export async function invitePath(rq: APIRequestContext, pr: number): Promise<string> {
  const c = await until(async () => (await control.comments(rq, pr)).find((x) => inviteRe.test(x.body)) ?? null, `invite comment on #${pr}`)
  return c.body.match(inviteRe)![1]
}

/** Verifies GitHub as person and connects Buffer as their own account; lands on approve. */
export async function verifyAndConnect(page: Page, rq: APIRequestContext, path: string, who: { id: number; login: string }) {
  await control.githubUser(rq, who.id, who.login)
  await control.bufferAccount(rq, who.login)
  await page.goto(path)
  await page.getByRole('link', { name: "Verify it's you with GitHub" }).click()
  await expect(page).toHaveURL(new RegExp(`${path}$`))
  await page.getByRole('link', { name: 'Connect Buffer & share' }).click()
  await expect(page).toHaveURL(new RegExp(`${path}/approve$`))
  await expect(page.getByRole('textbox', { name: 'Your post' }).first()).toBeVisible()
}

/** A fresh browser context, as a second person on another device. */
export async function newPerson(browser: Browser, baseURL: string | undefined) {
  const ctx = await browser.newContext({ baseURL })
  return { ctx, page: await ctx.newPage() }
}

/** Asserts a scheduled time is inside the launch window (5 minutes after launch_at to the window's end). */
export function expectInWindow(dueAt: string | undefined, l: LaunchRow) {
  expect(dueAt, 'dueAt').toBeTruthy()
  const due = Date.parse(dueAt!)
  const start = Date.parse(l.launchAt!)
  expect(due).toBeGreaterThanOrEqual(start + 5 * 60_000)
  expect(due).toBeLessThanOrEqual(start + l.windowMinutes * 60_000)
}
