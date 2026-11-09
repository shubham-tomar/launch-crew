// Launch images, end to end: the captain adds two images on New launch, they are
// stored as uploaded and shown on the launch page, and every captain post carries
// the card then the images; with the card switch off, only the images.
import { expect, test } from '@playwright/test'
import { control, launchButton, localInput, signInCaptain, until, type Post } from './fixtures/app.ts'
import { png } from './fixtures/png.ts'

const files = [
  { name: 'one.png', mimeType: 'image/png', buffer: png(200, 30, 30) },
  { name: 'two.png', mimeType: 'image/png', buffer: png(30, 30, 200) },
]

async function createWithImages(page: import('@playwright/test').Page, title: string) {
  await page.goto('/captain/new')
  await page.getByLabel('Title').fill(title)
  await page.getByLabel('Launch time').fill(localInput(60))
  await page.getByLabel('Add images').setInputFiles(files)
  await expect(page.getByRole('img', { name: 'Image 2' })).toBeVisible()
  await page.getByRole('button', { name: 'Create launch' }).click()
  await expect(page).toHaveURL(/\/captain\/launches\/[0-9a-f-]+$/)
  // The stored images, served from the fake R2.
  await expect(page.getByRole('img', { name: 'Image 2' })).toHaveAttribute('src', /\/uploads\/[0-9a-f]{64}\.png$/)
}

const captainPosts = async (rq: import('@playwright/test').APIRequestContext) =>
  (await control.posts(rq)).filter((p) => p.channelId.startsWith('captain-'))

async function launchAndCollect(page: import('@playwright/test').Page, rq: import('@playwright/test').APIRequestContext, before: number): Promise<Post[]> {
  await expect(launchButton(page)).toHaveText('Launch')
  await launchButton(page).click()
  await expect(launchButton(page)).toHaveText('Launched')
  const all = await until(async () => {
    const ps = await captainPosts(rq)
    return ps.length >= before + 2 ? ps : null
  }, 'two new captain posts')
  return all.slice(before)
}

test('captain posts carry the card then the images', async ({ page, request }) => {
  await control.reset(request)
  await signInCaptain(page, request)
  await createWithImages(page, 'Rocket meetup')
  const posts = await launchAndCollect(page, request, 0)
  for (const p of posts) {
    expect(p.assets, p.channelId).toHaveLength(3)
    expect(p.assets[0]).toMatch(/\/cards\/[0-9a-f]{64}\.png$/)
    expect(p.assets[1]).toMatch(/\/uploads\/[0-9a-f]{64}\.png$/)
    expect(p.assets[2]).toMatch(/\/uploads\/[0-9a-f]{64}\.png$/)
    expect(p.assets[1]).not.toBe(p.assets[2])
  }
})

test('with the card switch off, posts carry only the images', async ({ page, request }) => {
  await control.reset(request)
  await signInCaptain(page, request)
  await createWithImages(page, 'Rocket meetup, no card')
  await page.getByRole('switch', { name: 'Attach the launch card' }).click()
  await expect(page.getByRole('switch', { name: 'Attach the launch card' })).not.toBeChecked()
  const posts = await launchAndCollect(page, request, 0)
  for (const p of posts) {
    expect(p.assets, p.channelId).toHaveLength(2)
    for (const a of p.assets) expect(a).toMatch(/\/uploads\/[0-9a-f]{64}\.png$/)
  }
})
