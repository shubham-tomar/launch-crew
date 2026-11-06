// Submission screenshots. Not part of the default run:
//   SCREENSHOTS=1 npx playwright test --project=chromium
// Writes docs/submission/screenshots/<screen>-<width>-<scheme>.png.
import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test, type Browser, type Page } from '@playwright/test'
import { control, launchButton, setLaunchTime } from './fixtures/app.ts'
import { buildWorld, type World } from './fixtures/world.ts'

const out = join(dirname(fileURLToPath(import.meta.url)), '..', 'docs', 'submission', 'screenshots')
const sizes = [
  { width: 1440, height: 900 },
  { width: 375, height: 812 },
]
const schemes = ['light', 'dark'] as const

let world: World
let showPath = ''

test.skip(!process.env.SCREENSHOTS, 'set SCREENSHOTS=1')

test.beforeAll(async ({ browser, request, baseURL }) => {
  mkdirSync(out, { recursive: true })
  await control.reset(request)
  world = await buildWorld(browser, request, baseURL)

  // A launched manual launch gives the open link behind the QR slide.
  const ctx = await browser.newContext({ storageState: world.captain, baseURL })
  const page = await ctx.newPage()
  const res = await page.request.post('/api/captain/launches', { data: { title: 'Rocket meetup' } })
  const { id } = (await res.json()) as { id: string }
  await page.goto(`/captain/launches/${id}`)
  await setLaunchTime(page, 60)
  await launchButton(page).click()
  await expect(launchButton(page)).toHaveText('Launched')
  showPath = new URL(await page.getByLabel('Add by link').inputValue()).pathname + '/show'
  await ctx.close()
})

/** Opens url at a size and scheme, waits for ready, saves one PNG. */
async function shoot(
  browser: Browser,
  state: World['captain'] | undefined,
  url: string,
  name: string,
  ready: (p: Page) => Promise<void>,
  target?: (p: Page) => ReturnType<Page['locator']>,
) {
  for (const { width, height } of sizes) {
    for (const scheme of schemes) {
      const ctx = await browser.newContext({ storageState: state, viewport: { width, height } })
      await ctx.addInitScript((s) => localStorage.setItem('lc.scheme', s), scheme)
      const page = await ctx.newPage()
      await page.goto(url)
      await ready(page)
      await expect(page.locator('html')).toHaveAttribute('data-scheme', scheme)
      await page.evaluate(() => document.fonts.ready)
      const file = join(out, `${name}-${width}-${scheme}.png`)
      if (target) await target(page).screenshot({ path: file, type: 'png' })
      else await page.screenshot({ path: file, type: 'png' })
      await ctx.close()
    }
  }
}

test('home', async ({ browser }) => {
  await shoot(browser, undefined, '/', 'home', (p) =>
    expect(p.getByRole('link', { name: 'Sign in with Buffer' })).toBeVisible())
})

test('invite', async ({ browser }) => {
  await shoot(browser, world.alice, world.alicePath, 'invite', (p) =>
    expect(p.getByRole('link', { name: 'Continue' })).toBeVisible())
})

test('not on Buffer fallback', async ({ browser }) => {
  await shoot(browser, world.alice, `${world.alicePath}/fallback`, 'fallback', (p) =>
    expect(p.getByRole('button', { name: /Copy/ }).first()).toBeVisible())
})

test('approve', async ({ browser }) => {
  await shoot(browser, world.alice, `${world.alicePath}/approve`, 'approve', async (p) => {
    await expect(p.getByRole('textbox', { name: 'Your post' }).first()).toBeVisible()
    await expect(p.getByRole('button', { name: 'tune' })).toBeVisible()
  })
})

test('done', async ({ browser }) => {
  await shoot(browser, world.alice, `${world.alicePath}/done`, 'done', async (p) => {
    await expect(p.getByRole('link', { name: 'Open in Buffer' })).toBeVisible()
    await expect(p.locator('[aria-busy="true"]')).toHaveCount(0)
  })
})

test('captain launched', async ({ browser }) => {
  await shoot(browser, world.captain, `/captain/launches/${world.launchId}`, 'captain-launched', (p) =>
    expect(p.getByRole('button', { name: 'Launched' })).toBeVisible())
})

test('captain green draft', async ({ browser }) => {
  const ctx = await browser.newContext({ storageState: world.captain })
  const res = await ctx.request.post('/api/captain/launches', { data: { title: 'Rocket 1.3' } })
  const { id } = (await res.json()) as { id: string }
  const at = new Date(Date.now() + 3600_000).toISOString()
  expect((await ctx.request.patch(`/api/captain/launches/${id}`, { data: { launchAt: at } })).ok()).toBe(true)
  await ctx.close()
  await shoot(browser, world.captain, `/captain/launches/${id}`, 'captain-green', (p) =>
    expect(p.getByRole('button', { name: 'Launch', exact: true })).toBeEnabled())
})

test('card image', async ({ browser }) => {
  await shoot(browser, world.alice, world.alicePath, 'card', (p) =>
    expect(p.locator('img.card-img, .card').first()).toBeVisible(), (p) => p.locator('img.card-img, .card').first())
})

test('qr slide', async ({ browser }) => {
  await shoot(browser, undefined, showPath, 'qr', (p) => expect(p.locator('canvas')).toBeVisible())
})
