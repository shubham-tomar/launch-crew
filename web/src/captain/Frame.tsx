import type { ReactNode } from 'react'
import { NavLink } from 'react-router'
import NavPill from '../components/NavPill'
import SchemeSwitch from '../components/SchemeSwitch'
import Shell from '../components/Shell'
import AccountMenu from './AccountMenu'
import './captain.css'

// Skip link, floating nav pill (brand, nav, account menu), the one <main>, and the appearance switch.
export default function Frame({ children }: { children: ReactNode }) {
  return (
    <div className="cap-page">
      <a className="cap-skip" href="#main">
        Skip to main content
      </a>
      <div className="cap-top">
        <NavPill
          home="/captain"
          links={
            <>
              <li>
                <NavLink to="/captain" end>
                  Launches
                </NavLink>
              </li>
              <li>
                <NavLink to="/captain/repos">Repos</NavLink>
              </li>
              <li>
                <NavLink to="/captain/templates">Templates</NavLink>
              </li>
            </>
          }
          end={
            <>
              <SchemeSwitch />
              <AccountMenu />
            </>
          }
        />
      </div>
      <Shell width="captain" id="main">
        {children}
      </Shell>
    </div>
  )
}
