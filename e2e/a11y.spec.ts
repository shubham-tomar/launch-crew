// Spec 5.16: axe on every 5.12 state reachable through the fakes, in light
// and dark; no serious or critical violation. Tests run in file order: the
// later ones close the window and expire the invites.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Browser, type Page } from '@playwright/test'
import { control, people, until } from './fixtures/app.ts'
import { buildWorld, type World } from './fixtures/world.ts'

let world: World

test.beforeAll(async ({ browser, request, baseURL }) => {
  await control.reset(request)
  world = await buildWorld(browser, request, baseURL)
})

type State = World['captain'] | undefined

async function open(browser: Browser, storageState: State) {
  const ctx = await browser.newContext({ storageState })
  return { ctx, page: await ctx.newPage() }
}

/** Loads url in light then dark and runs axe on each; ready names what must show first. */
async function scan(page: Page, url: string, ready: (p: Page) => Promise<void>) {
  for (const scheme of ['light', 'dark'] as const) {
    await page.goto(url)
    await page.evaluate((s) => localStorage.setItem('lc.scheme', s), scheme)
    await page.reload()
    await ready(page)
    await expect(page.locator('html')).toHaveAttribute('data-scheme', scheme)
    const { violations } = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
      .analyze()
    const bad = violations
      .filter((v) => v.impact === 'serious' || v.impact === 'critical')
      .map((v) => `${v.impact} ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)
    expect(bad, `${url} (${scheme})`).toEqual([])
  }
}

const h1 = (text: string | RegExp) => async (p: Page) => {
  await expect(p.getByRole('heading', { level: 1 })).toHaveText(text)
}

test('home', async ({ browser }) => {
  const { ctx, page } = await open(browser, undefined)
  await scan(page, '/', async (p) => {
    await expect(p.getByRole('link', { name: 'Sign in with Buffer' }).first()).toBeVisible()
    // Parts of the page unfold as they scroll into view; scan them as a reader sees them, unfolded.
    for (const s of await p.locator('.home-ticket > .home-section').all()) {
      await s.scrollIntoViewIfNeeded()
      await expect(s).toHaveClass(/is-in/)
    }
    await p.waitForTimeout(1500)
  })
  await ctx.close()
})

test('invite: before verification (generic card)', async ({ browser }) => {
  const { ctx, page } = await open(browser, undefined)
  await scan(page, world.bobPath, async (p) => {
    await expect(p.getByRole('link', { name: "Verify it's you with GitHub" })).toBeVisible()
  })
  await ctx.close()
})

test('invite: contributor', async ({ browser }) => {
  const { ctx, page } = await open(browser, world.alice)
  await scan(page, world.alicePath, async (p) => {
    await expect(p.getByRole('link', { name: 'Continue' })).toBeVisible()
  })
  await ctx.close()
})

test('invite: made for someone else', async ({ browser }) => {
  const { ctx, page } = await open(browser, undefined)
  await scan(page, `${world.alicePath}?foreign=1`, h1('This link was made for someone else'))
  await ctx.close()
})

test('not on Buffer fallback', async ({ browser }) => {
  const { ctx, page } = await open(browser, world.alice)
  await scan(page, `${world.alicePath}/fallback`, async (p) => {
    await expect(p.getByRole('button', { name: /Copy/ }).first()).toBeVisible()
  })
  await ctx.close()
})

test('approve: drafts', async ({ browser }) => {
  const { ctx, page } = await open(browser, world.alice)
  await scan(page, `${world.alicePath}/approve`, async (p) => {
    await expect(p.getByRole('textbox', { name: 'Your post' }).first()).toBeVisible()
  })
  await ctx.close()
})

test('approve: no eligible channels', async ({ browser, request }) => {
  const { ctx, page } = await open(browser, undefined)
  await control.githubUser(request, people.bob.id, people.bob.login)
  await control.bufferAccount(request, 'empty-bob')
  await page.goto(world.bobPath)
  await page.getByRole('link', { name: "Verify it's you with GitHub" }).click()
  await page.getByRole('link', { name: 'Connect Buffer & share' }).click()
  await expect(page).toHaveURL(/\/approve$/)
  await scan(page, `${world.bobPath}/approve`, async (p) => {
    await expect(p.getByRole('button', { name: 'Copy text' })).toBeVisible()
  })
  await ctx.close()
})

test('done and crew board', async ({ browser }) => {
  const { ctx, page } = await open(browser, world.alice)
  await scan(page, `${world.alicePath}/done`, async (p) => {
    await expect(p.getByRole('link', { name: 'Open in Buffer' })).toBeVisible()
    await expect(p.locator('[aria-busy="true"]')).toHaveCount(0)
  })
  await ctx.close()
})

test('captain: no launches yet', async ({ browser, request }) => {
  const { ctx, page } = await open(browser, undefined)
  await control.bufferAccount(request, 'newcaptain')
  await page.goto('/auth/buffer/start?return_to=/captain')
  await expect(page).toHaveURL(/\/captain$/)
  await scan(page, '/captain', async (p) => {
    await expect(p.getByRole('heading', { name: /No launches yet/ })).toBeVisible()
  })
  await ctx.close()
})

test('captain: repos and new launch', async ({ browser }) => {
  const { ctx, page } = await open(browser, world.captain)
  await scan(page, '/captain/repos', async (p) => {
    await expect(p.getByText('acme/rocket')).toBeVisible()
  })
  await scan(page, '/captain/new', h1('New launch'))
  await ctx.close()
})

test('captain: draft red, then green', async ({ browser }) => {
  const { ctx, page } = await open(browser, world.captain)
  const res = await page.request.post('/api/captain/launches', { data: { title: 'Meetup draft' } })
  expect(res.ok()).toBe(true)
  const { id } = (await res.json()) as { id: string }
  await scan(page, `/captain/launches/${id}`, async (p) => {
    await expect(p.getByRole('button', { name: /^Launch · \d checks? left$/ })).toBeDisabled()
  })
  const at = new Date(Date.now() + 3600_000).toISOString()
  expect((await page.request.patch(`/api/captain/launches/${id}`, { data: { launchAt: at } })).ok()).toBe(true)
  await scan(page, `/captain/launches/${id}`, async (p) => {
    await expect(p.getByRole('button', { name: 'Launch', exact: true })).toBeEnabled()
  })
  await ctx.close()
})

test('captain: launched', async ({ browser }) => {
  const { ctx, page } = await open(browser, world.captain)
  await scan(page, `/captain/launches/${world.launchId}`, async (p) => {
    await expect(p.getByRole('button', { name: 'Launched' })).toBeVisible()
  })
  await ctx.close()
})

test('captain: dead-lettered job', async ({ browser, request }) => {
  await control.deadLetter(request, world.launchId)
  const { ctx, page } = await open(browser, world.captain)
  await scan(page, `/captain/launches/${world.launchId}`, async (p) => {
    await expect(p.getByRole('alert').filter({ hasText: 'Step failed' })).toBeVisible()
  })
  await ctx.close()
})

test('settings: signed in, and nothing stored', async ({ browser }) => {
  const a = await open(browser, world.alice)
  await scan(a.page, '/settings', h1('Settings'))
  await a.ctx.close()
  const b = await open(browser, undefined)
  await scan(b.page, '/settings', h1('Settings'))
  await b.ctx.close()
})

test('approve: launch closed', async ({ browser, request }) => {
  await until(async () => (await control.closeWindow(request, world.launchId)).rows === 1, 'close_window job')
  await until(async () => (await control.launches(request)).find((l) => l.id === world.launchId)?.status === 'closed', 'launch closed')
  const { ctx, page } = await open(browser, world.alice)
  await scan(page, `${world.alicePath}/approve`, async (p) => {
    await expect(p.getByRole('radio', { name: 'next free slot' })).toBeVisible()
    await expect(p.getByRole('radio', { name: 'Land in the window' })).toHaveCount(0)
  })
  await ctx.close()
})

test('invite: expired', async ({ browser, request }) => {
  await control.expireInvites(request)
  const { ctx, page } = await open(browser, undefined)
  await scan(page, world.bobPath, h1('This link has expired'))
  await ctx.close()
})
