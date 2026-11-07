// Spec 7, end to end: captain. Install with a bound state, release webhook,
// the worker builds the launch, a 404 card turns preflight red and blocks
// Launch, fix, Launch creates the captain posts and PR comments once; a
// second press creates nothing new.
import { expect, test } from '@playwright/test'
import { control, installApp, launchButton, people, releaseLaunch, setLaunchTime, signInCaptain, until } from './fixtures/app.ts'

test('captain launches once', async ({ page, request }) => {
  await control.reset(request)
  await signInCaptain(page, request)

  // A callback whose state is not bound to this session is refused.
  const forged = await page.request.get('/github/install/callback?installation_id=4242&state=forged', { maxRedirects: 0 })
  expect(forged.status()).toBe(403)
  await installApp(page, request)

  const id = await releaseLaunch(request)
  await page.goto('/captain')
  await page.getByRole('link', { name: 'Rocket 1.2' }).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Rocket 1.2')
  await expect(page.getByText('Stream large uploads without buffering').first()).toBeVisible()

  // Red: the card URL answers 404 (and no launch time yet).
  await control.r2Missing(request, true)
  await page.reload()
  const cardRow = page.locator('.cap-section', { hasText: 'Preflight' }).getByRole('listitem').filter({ hasText: 'Card images reachable' })
  await expect(cardRow).toContainText('needs a fix')
  await expect(cardRow).toContainText('could not be fetched (HTTP 404)')
  await expect(launchButton(page)).toBeDisabled()
  await expect(launchButton(page)).toHaveText(/^Launch · \d checks? left$/)

  // Fix both, then Launch.
  await control.r2Missing(request, false)
  await setLaunchTime(page, 60)
  await page.reload()
  await expect(cardRow).toContainText('ready')
  await expect(launchButton(page)).toHaveText('Launch')
  await launchButton(page).click()
  await expect(launchButton(page)).toHaveText('Launched')

  const captainPosts = async () => (await control.posts(request)).filter((p) => p.channelId.startsWith('captain-'))
  await until(async () => (await captainPosts()).length === 2, 'two captain posts')
  await until(async () => (await control.comments(request, people.bob.pr)).length === 1, 'comment on #12')
  await until(async () => (await control.comments(request, people.alice.pr)).length === 1, 'comment on #11')
  // carol was suggested, not ticked: no invite. Her PR is the release's latest, so it
  // carries the one "draft is ready" mention for the captain, posted before launch.
  const on13 = await control.comments(request, 13)
  expect(on13.filter((c) => !c.body.includes('launchcrew:draft-ready'))).toHaveLength(0)
  expect(on13.filter((c) => c.body.startsWith('@captain your Launch Crew draft'))).toHaveLength(1)

  // Second press: refused, and nothing new reaches Buffer or GitHub.
  const again = await page.request.post(`/api/captain/launches/${id}/launch`)
  expect(again.status()).toBe(409)
  await page.waitForTimeout(3000) // give the worker a chance to misbehave
  expect(await captainPosts()).toHaveLength(2)
  expect(await control.comments(request, people.alice.pr)).toHaveLength(1)
  expect(await control.comments(request, people.bob.pr)).toHaveLength(1)
})
