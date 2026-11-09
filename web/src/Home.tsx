import { useEffect, useRef, type ReactNode } from 'react'
import NavPill from './components/NavPill'
import Pass from './components/Pass'
import Plane from './components/Plane'
import ServiceIcon from './components/ServiceIcon'
import SchemeSwitch from './components/SchemeSwitch'
import Wordmark from './components/Wordmark'
import './home.css'

const SIGN_IN = '/auth/buffer/start?return_to=/captain'

function Tick({ warn }: { warn?: boolean }) {
  return (
    <span className={warn ? 'home-tick home-tick-warn' : 'home-tick'} aria-hidden="true">
      <svg viewBox="0 0 12 12">{warn ? <path d="M6 2.5v4.2M6 9.2v.1" /> : <path d="M2 6.3 L5 9 L10 3" />}</svg>
    </span>
  )
}

// A tear line between two parts of the pass, with a notch of the page at each edge.
function Perf() {
  return <div className="perf perf-h home-perf" aria-hidden="true" />
}

// One part of the pass. Its tear line is its bottom edge, so the line travels with
// the part when it folds; the last part, the stub, has none.
function Section({ id, className, last, children }: { id?: string; className?: string; last?: boolean; children: ReactNode }) {
  return (
    <section id={id} className={['home-section', className].filter(Boolean).join(' ')}>
      {children}
      {!last && <Perf />}
    </section>
  )
}

const PROBLEMS = [
  ['One account posts.', 'The announcement goes out once, from the org or the organiser, and that is the whole launch.'],
  ['Everyone else reposts it.', 'The same sentence, in nine feeds, in nine voices that all sound like one.'],
  ['Or they stay quiet.', 'Writing your own take means a blank box and ten minutes nobody has on the day.'],
]

// The three ways a launch starts. Each ends the same way: a draft per person, approved into their own Buffer.
const WAYS: { tag: string; title: string; body: string; who: string }[] = [
  {
    tag: 'GitHub release',
    title: 'Ship a release',
    body: 'Publish a release and a draft launch is waiting. The crew is everyone with a merged PR in it, and each gets a comment on their PR with their invite.',
    who: 'Contributor · Reviewer · Maintainer',
  },
  {
    tag: 'Meetup or talk',
    title: 'Put a QR on the screen',
    body: 'Show the event slide at the end of your talk. People scan it, join the crew and get a draft about what they heard, in their own words.',
    who: 'Speaker · Organiser · Attendee',
  },
  {
    tag: 'Manual launch',
    title: 'Share one link',
    body: 'Launching a product, a post or anything else? Write the key facts once, share the link, and everyone you send it to posts their own take.',
    who: 'Your team · friends · users',
  },
]

// What the captain gets beyond the drafts.
const CAPTAIN: { title: string; body: string }[] = [
  { title: 'Your look on every card', body: 'Logo, background, text and accent per repo, with a live preview before you save.' },
  { title: 'Templates', body: 'Name a few: the PR comment and the notes the drafts follow. Pick one per repo or per launch.' },
  { title: 'See every post land', body: 'Once a post goes out, the crew list links straight to it. One that failed is flagged.' },
  { title: 'Preflight', body: 'Limits, network rules and card images are checked before the launch button turns on.' },
]

const STEPS = [
  ['A launch starts', 'A GitHub release lands, a QR goes up at your talk, or you share a link. The crew forms from there.'],
  ['Drafts built in each voice', "One draft per person, written from that person's own past posts. Their role changes how it opens."],
  ['Crew approves on their phone', 'One link, one tap. They can edit it first, or not send it at all.'],
  ["Everyone's queue fills", "Each approved post goes into that person's own Buffer queue, spread across the launch window."],
]

const CREW: [string, string, string, 'ok' | 'wip' | 'new', string][] = [
  ['MK', 'Maya Kapoor', '#482', 'ok', 'Approved'],
  ['SO', 'Sam Okonkwo', '#468', 'ok', 'Approved'],
  ['HB', 'Hannah Bergström-Lindqvist', '#489', 'wip', 'Drafting'],
  ['RM', 'Rafael Montenegro-Silva', '#492', 'new', 'Invited'],
]

const CONTROL = [
  'Launch Crew writes one post into your own queue, and nothing else.',
  'It never reads or touches anything else in that queue.',
  'Disconnect whenever you like. The post stays yours.',
  'Nothing is sent without your tap, and you can edit the draft first.',
]

const PREFLIGHT = [
  'Contributor card images reachable',
  'Daily posting limits respected',
  "Each channel's network rules checked",
  'LinkedIn link goes in the first comment',
  'No overlapping drafts in any queue',
  'Buffer rate headroom',
]

// The page unfolds like a paper pass, accordion style. Each part hangs from the
// crease at the bottom of the part above and swings down from its top edge as
// it scrolls up the screen, flat by OPEN_AT. Parts alternate: one comes from
// behind the part above, the next waits until that part is flat and then swings
// down over its front from the top, fading in from clear like glass. The
// angle follows scroll position, eased toward it each frame so it glides
// rather than jumps; scrolling back folds it away again. Skipped for reduced
// motion, so the page is always flat and readable.
const FOLD_BEHIND = 118 // degrees: past 90, so a folded part sits behind the one above
const FOLD_OVER = 168 // degrees: nearly flat, lying face down over the part above
const START_AT = 1 // starts opening as its crease enters the bottom of the viewport
const OPEN_AT = 0.32 // flat once its crease is this far down the viewport
const MIN_SPAN = 0.25 // least share of the viewport scrolled while a part opens
const EASE = 0.12 // share of the remaining distance covered each frame

function useFold() {
  const ticket = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const root = ticket.current
    if (!root) return
    if (typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const parts = Array.from(root.querySelectorAll<HTMLElement>(':scope > .home-section'))
    const shown = parts.map(() => -1) // eased openness per part; -1 until first drawn
    let frame = 0
    // Where each crease sits on the page. Measured from layout (offsetTop), which the
    // fold's own rotation doesn't change; getBoundingClientRect would report the
    // rotated box and open a flipped part far too early.
    let creases: number[] = []
    const measure = () => {
      creases = parts.map((el) => {
        let y = 0
        for (let e: HTMLElement | null = el; e; e = e.offsetParent as HTMLElement | null) y += e.offsetTop
        return y
      })
    }

    const over = (i: number) => i % 2 === 1

    // How open each part should be for the current scroll position, 0 to 1. The
    // parts open in turn: each begins once its crease is on screen and the part
    // above is flat, and lands flat when its crease reaches OPEN_AT, taking at
    // least MIN_SPAN of scrolling. So nothing opens out of order, hangs detached
    // below a part still moving, or falls behind further down the page.
    const targets = () => {
      const vh = window.innerHeight
      const y = window.scrollY
      const atEnd = y + vh >= document.documentElement.scrollHeight - 2
      let prevFlat = -Infinity // scroll position at which the part above lies flat
      return parts.map((_, i) => {
        const begin = Math.max(prevFlat, creases[i] - vh * START_AT)
        const span = Math.max(creases[i] - vh * OPEN_AT - begin, vh * MIN_SPAN)
        prevFlat = begin + span
        if (atEnd) return 1
        const t = Math.min(1, Math.max(0, (y - begin) / span))
        // From behind: quick off the crease, settling flat. Over the front: lifts
        // gently, swings, settles.
        return over(i) ? t * t * (3 - 2 * t) : 1 - (1 - t) ** 3
      })
    }

    const paint = (el: HTMLElement, open: number, i: number) => {
      const deg = (over(i) ? FOLD_OVER : -FOLD_BEHIND) * (1 - open)
      el.style.setProperty('--fold', `${deg.toFixed(2)}deg`)
      // Past upright, a part folding over shows the plain back of the paper, lit
      // softly; facing you, it darkens the more it is turned away.
      const back = deg > 90
      const shade = back ? 0.06 + 0.1 * ((deg - 90) / 90) : 0.32 * Math.min(1, (1 - open) * 1.6)
      el.style.setProperty('--shade', shade.toFixed(3))
      el.style.setProperty('--back', back ? '1' : '0')
      // A part folding over is clear until it starts to move and turns solid like
      // glass catching the light as it swings down; one from behind is always solid.
      el.style.setProperty('--glass', over(i) ? Math.min(1, open / 0.45).toFixed(3) : '1')
      el.classList.toggle('is-in', open === 1)
    }

    const tick = () => {
      frame = 0
      const want = targets()
      let moving = false
      parts.forEach((el, i) => {
        const cur = shown[i] < 0 ? want[i] : shown[i]
        let next = cur + (want[i] - cur) * EASE
        if (Math.abs(want[i] - next) < 0.002) next = want[i]
        else moving = true
        shown[i] = next
        paint(el, next, i)
      })
      if (moving) frame = requestAnimationFrame(tick)
    }
    const queue = () => {
      if (!frame) frame = requestAnimationFrame(tick)
    }
    let live = true
    const remeasure = () => {
      if (!live) return
      measure()
      queue()
    }

    root.classList.add('home-fold')
    parts.forEach((el, i) => {
      // Behind the part above, or over it.
      el.style.zIndex = String(over(i) ? 100 + i : parts.length - i)
      el.classList.toggle('home-fold-over', over(i))
    })
    measure()
    tick()
    window.addEventListener('scroll', queue, { passive: true })
    window.addEventListener('resize', remeasure)
    document.fonts?.ready.then(remeasure) // web fonts can shift the layout after first paint
    return () => {
      live = false
      cancelAnimationFrame(frame)
      window.removeEventListener('scroll', queue)
      window.removeEventListener('resize', remeasure)
      root.classList.remove('home-fold')
      for (const el of parts) {
        el.style.removeProperty('--fold')
        el.style.removeProperty('--shade')
        el.style.removeProperty('--back')
        el.style.removeProperty('--glass')
        el.style.removeProperty('z-index')
        el.classList.remove('is-in', 'home-fold-over')
      }
    }
  }, [])
  return ticket
}

// Public root: what this is, how it works, and the way in.
export default function Home() {
  const ticket = useFold()
  return (
    <main className="home">
      <div className="home-wrap">
        <NavPill
          links={
            <>
              <li><a href="#ways">Ways to launch</a></li>
              <li><a href="#how">How it works</a></li>
              <li><a href="#captain">For captains</a></li>
              <li><a href="#privacy">Privacy</a></li>
            </>
          }
          end={<a className="btn btn-primary" href={SIGN_IN}>Sign in with Buffer</a>}
        />

        <div className="home-ticket" ref={ticket}>
        <header className="home-hero">
          <div className="home-hero-copy">
            <span className="eyebrow">Releases · meetups · launches</span>
            <h1 className="home-title">One launch. Nine people. Nine posts, all theirs.</h1>
            <p className="home-lede">
              A GitHub release, a meetup talk or a launch of your own. Everyone who was part of it gets a draft in their own voice, from their own past
              posts, and approves it into their own Buffer queue.
            </p>
            <div className="home-cta">
              <a className="btn btn-primary" href={SIGN_IN}>
                <Plane className="home-cta-plane" />
                Start a launch
              </a>
              <a className="btn btn-secondary" href="#how">See how it works</a>
            </div>
            <p className="home-assure">Nothing posts until each person taps approve. Sign-in is with Buffer.</p>
          </div>

          <div className="home-stack" aria-hidden="true">
            <div className="home-stack-cards">
              <div className="home-stack-back home-stack-back-a">
                <Pass eyebrow="v2.3.0" name="Wei Chen" meta="@weichen · Reviewer" stub={{ label: 'Crew', value: '7', date: '3 Nov' }} />
              </div>
              <div className="home-stack-back home-stack-back-b">
                <Pass eyebrow="Meetup #12" name="Priya Nair" meta="@priyan · Speaker" stub={{ label: 'Crew', value: '38', date: '6 Nov' }} />
              </div>
              <div className="home-stack-front">
                <Pass
                  eyebrow="v2.4.0 · PR #482"
                  name="Maya Kapoor"
                  meta="@mayakp · Contributor"
                  footer="shubham-tomar/launch-crew"
                  stub={{ label: 'Crew', value: '9', date: '6 Nov' }}
                />
              </div>
            </div>
            <p className="home-stack-note">Everyone gets a card like this, 1200 × 630, attached to their post.</p>
          </div>
          <Perf />
        </header>

        <Section id="ways">
          <h2 className="home-h2">Three ways to start a launch</h2>
          <ol className="home-ways">
            {WAYS.map((w) => (
              <li className="panel home-way" key={w.tag}>
                <span className="eyebrow">{w.tag}</span>
                <h3 className="home-h3">{w.title}</h3>
                <p>{w.body}</p>
                <span className="home-way-who">{w.who}</span>
              </li>
            ))}
          </ol>
          <p className="home-quiet">However it starts, it ends the same way: one draft per person, and nothing posts without their tap.</p>
        </Section>

        <Section id="why">
          <h2 className="home-h2">Launch day usually looks like this</h2>
          <ol className="home-problems">
            {PROBLEMS.map(([lead, body], i) => (
              <li className="panel home-problem" key={lead}>
                <span className="home-num">{i + 1}</span>
                <h3 className="home-h3">{lead}</h3>
                <p>{body}</p>
              </li>
            ))}
          </ol>
        </Section>

        <Section id="how">
          <h2 className="home-h2">How it works</h2>
          <ol className="home-steps">
            {STEPS.map(([title, body], i) => (
              <li className="panel home-step" key={title}>
                <span className="eyebrow">Step {['one', 'two', 'three', 'four'][i]}</span>
                <div className="home-ill" aria-hidden="true">
                  {i === 0 && (
                    <>
                      <span className="home-ill-chip">v2.4.0</span>
                      <span className="home-ill-line" />
                      <span className="home-ill-line home-ill-short" />
                    </>
                  )}
                  {i === 1 && (
                    <>
                      <span className="home-ill-line" />
                      <span className="home-ill-line" />
                      <span className="home-ill-line home-ill-short" />
                    </>
                  )}
                  {i === 2 && <span className="home-ill-btn">Add to my queue</span>}
                  {i === 3 && (
                    <>
                      <span className="home-ill-row" />
                      <span className="home-ill-row" />
                      <span className="home-ill-row" />
                    </>
                  )}
                </div>
                <h3 className="home-h3">{title}</h3>
                <p>{body}</p>
              </li>
            ))}
          </ol>
        </Section>

        <Section>
          <h2 className="home-h2">Two screens, two people</h2>
          <div className="home-two">
            <figure className="home-fig">
              <div className="panel home-roster" aria-hidden="true">
                <div className="home-roster-head">
                  <span className="home-h3">The crew · 9</span>
                  <span className="home-pill-ok">6 approved</span>
                </div>
                <ul>
                  {CREW.map(([ini, name, pr, tone, state]) => (
                    <li key={name} className="home-roster-row">
                      <span className="home-avatar">{ini}</span>
                      <span className="home-roster-name">{name}</span>
                      <span className="home-roster-pr">{pr}</span>
                      <span className={`home-state home-state-${tone}`}>{state}</span>
                    </li>
                  ))}
                </ul>
              </div>
              <figcaption>
                The captain sees the whole crew on one page, invited, drafting or approved, and can only launch once preflight passes.
              </figcaption>
            </figure>
            <figure className="home-fig">
              <div className="panel home-phone" aria-hidden="true">
                <span className="home-pill-ok">You are credited</span>
                <span className="home-h3">Split-pane resize with the keyboard</span>
                <div className="home-draft">
                  <p>keyboard split-pane resize shipped in v2.4.0 — alt+arrow nudges a divider, escape puts it back.</p>
                  <span className="home-count">128 / 280</span>
                </div>
                <div className="home-chips">
                  <span className="home-chip home-chip-on">X</span>
                  <span className="home-chip home-chip-on">LinkedIn</span>
                  <span className="home-chip">Threads</span>
                </div>
                <span className="home-ill-btn">Add to my queue</span>
              </div>
              <figcaption>The contributor opens one link, reads a draft that already sounds like them, and taps once.</figcaption>
            </figure>
          </div>
        </Section>

        <Section>
          <div className="home-voice-head">
            <h2 className="home-h2">Written in their voice</h2>
            <p>Maya writes short and lowercase. So does her draft.</p>
          </div>
          <div className="home-voice">
            <div className="panel">
              <span className="home-label">Her last two posts</span>
              <p className="home-quote">spent the afternoon making the focus ring survive a drag. tiny thing, feels so much better.</p>
              <p className="home-quote">shipped my first PR to a repo i actually use every day. weirdly emotional about it.</p>
            </div>
            <div className="home-voice-arrow" aria-hidden="true">
              <Plane deg={90} className="home-voice-plane" />
            </div>
            <div className="panel home-voice-out">
              <span className="home-label">The draft it produced</span>
              <p className="home-quote home-quote-hero">
                keyboard split-pane resize shipped in v2.4.0 — alt+arrow nudges a divider, escape puts it back. my first PR on this repo!
              </p>
              <div className="home-voice-meta">
                <span className="home-count">128 / 280</span>
                <span>Hers to edit, or not to send</span>
              </div>
            </div>
          </div>
        </Section>

        <Section id="captain">
          <h2 className="home-h2">For the captain</h2>
          <ul className="home-captain">
            {CAPTAIN.map((c, i) => (
              <li className="panel home-cap" key={c.title}>
                <div className="home-ill" aria-hidden="true">
                  {i === 0 && (
                    <span className="home-swatches">
                      {['#15181F', '#F4EFE6', '#4A45D1', '#2B643B', '#EC8669'].map((c) => (
                        <span key={c} style={{ backgroundColor: c }} />
                      ))}
                    </span>
                  )}
                  {i === 1 && (
                    <span className="home-tabs">
                      <span className="home-tab home-tab-on">Big releases</span>
                      <span className="home-tab">Meetups</span>
                    </span>
                  )}
                  {i === 2 && (
                    <span className="home-landed">
                      <ServiceIcon service="twitter" />
                      View on X
                    </span>
                  )}
                  {i === 3 && (
                    <span className="home-tabs">
                      <Tick />
                      <Tick />
                      <Tick />
                      <Tick warn />
                    </span>
                  )}
                </div>
                <h3 className="home-h3">{c.title}</h3>
                <p>{c.body}</p>
              </li>
            ))}
          </ul>
        </Section>

        <Section id="privacy" className="home-control">
          <div className="home-control-main">
            <h2 className="home-h2">You stay in control</h2>
            <ul className="home-checks">
              {CONTROL.map((line) => (
                <li key={line}>
                  <Tick />
                  <span>{line}</span>
                </li>
              ))}
            </ul>
            <p className="home-quiet">
              <a href="/settings">Opt out or delete your data</a>
            </p>
          </div>
          <div className="panel home-preflight">
            <span className="home-label">Preflight, before anything goes out</span>
            <ul className="home-checks">
              {PREFLIGHT.map((line) => (
                <li key={line}>
                  <Tick />
                  <span>{line}</span>
                </li>
              ))}
              <li>
                <Tick warn />
                <span>Launch time is in the past. Pick a later moment.</span>
              </li>
            </ul>
            <p className="home-quiet">Seven checks. The launch button stays off until they pass.</p>
          </div>
        </Section>

        <Section className="home-final" last>
          <div className="home-final-copy">
            <h2 className="home-h2 home-final-title">Run your next launch as a crew.</h2>
            <p>Nothing posts until each person taps approve.</p>
          </div>
          <a className="btn btn-secondary home-final-btn" href={SIGN_IN}>Sign in with Buffer</a>
        </Section>
        </div>

        <footer className="home-footer">
          <Wordmark />
          <ul>
            <li><a href="#privacy">Privacy</a></li>
            <li><a href="https://github.com/shubham-tomar/launch-crew">GitHub</a></li>
          </ul>
          <span>Built for Buffer Commit 2026</span>
          <SchemeSwitch />
        </footer>
      </div>
    </main>
  )
}
