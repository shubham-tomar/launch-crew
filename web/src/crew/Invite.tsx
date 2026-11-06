import { Link, useParams } from 'react-router'
import Card from '../components/Card'
import Skeleton from '../components/Skeleton'
import { CrewFrame, Dead, Foreign, useInvite } from './shared'

export default function Invite() {
  const token = useParams().token ?? ''
  const { view, error } = useInvite(token)

  if (error) return <Dead code={error.code} />
  if (view?.foreign) return <Foreign releaseUrl={view.launch.releaseUrl} />

  const q = encodeURIComponent
  return (
    <CrewFrame version={view?.launch.version} footer>
      <div className="crew-stack" aria-busy={!view || undefined}>
        {view ? (
          <Card
            project={view.launch.project}
            version={view.launch.version}
            title={view.launch.title}
            image={view.card}
          />
        ) : (
          <Skeleton lines={4} />
        )}
        <div className="perf perf-h crew-perf" aria-hidden="true" />
        {view ? <p className="crew-preview">{view.preview}</p> : <Skeleton lines={3} />}
        <p className="crew-caption">
          adds drafts to your queue only · nothing posts without your tap · disconnect any time
        </p>
        {view && (
          <div className="crew-actions">
            {view.needsVerify ? (
              <a className="btn btn-primary" href={`/github/verify/start?crew=${q(view.member.id)}&t=${q(token)}`}>
                Verify it's you with GitHub
              </a>
            ) : view.connected ? (
              <a className="btn btn-primary" href={`/s/${q(token)}/approve`}>
                Continue
              </a>
            ) : (
              <a className="btn btn-primary" href={`/auth/buffer/start?return_to=/s/${q(token)}/approve`}>
                Connect Buffer &amp; share
              </a>
            )}
            <Link className="btn btn-secondary" to={`/s/${q(token)}/fallback`}>
              I don't use Buffer
            </Link>
          </div>
        )}
      </div>
    </CrewFrame>
  )
}
