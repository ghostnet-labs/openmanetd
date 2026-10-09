// =============================================================================
// Layout.jsx — Lattice app shell (sidebar + bottom tabs)
// =============================================================================

import React, { useState, useEffect } from 'react';
import { NavLink, Outlet } from 'react-router-dom';
import { useAuth } from './contexts/useAuth.js';
import SetupDismissBanner from './components/SetupDismissBanner.jsx';
import NavIcon from './components/NavIcon.jsx';
import useLuciProxy, { LUCI_PATH } from './hooks/useLuciProxy.js';
import { MOBILE_BREAKPOINT } from './constants.js';
import './Layout.css';

// Nav items grouped by section. Operations = day-to-day use, System = admin.
const NAV_GROUPS = [
  {
    label: 'Operations',
    items: [
      { to: '/',          label: 'Dashboard', short: 'Home',  icon: 'dashboard' },
      { to: '/comms',     label: 'Comms',     short: 'Comms', icon: 'comms' },
      { to: '/topology',  label: 'Topology',  short: 'Topo',  icon: 'topology' },
      { to: '/gps',       label: 'GPS / GNSS', short: 'GPS',  icon: 'gps' },
      { to: '/blos',      label: 'BLOS',      short: 'BLOS',  icon: 'blos' },
    ],
  },
  {
    label: 'System',
    items: [
      { to: '/settings',  label: 'Settings',  short: 'Config', icon: 'settings' },
    ],
  },
];

// Bottom tab bar — 4 primary tabs plus a "More" overflow for the rest.
const PRIMARY_TABS = [
  { to: '/',         short: 'Home',  icon: 'dashboard' },
  { to: '/comms',    short: 'Comms', icon: 'comms' },
  { to: '/topology', short: 'Topo',  icon: 'topology' },
  { to: '/gps',      short: 'GPS',   icon: 'gps' },
];
// The Advanced entry is a plain link, not a NavLink: LuCI is served by the
// frontend daemon's reverse proxy, so this is a full-page handoff that leaves
// the SPA. Shown only when the daemon reports the proxy is enabled.
// LuCI keeps its own root login (the sign-ins are separate by design, D-037),
// so the entry says so up front: the LuCI login screen is expected, and the
// OpenMANET session is untouched, so Back returns without signing in again.
const ADVANCED_LABEL = 'Advanced';
const ADVANCED_DESC = 'LuCI · separate root login';
const ADVANCED_TITLE = 'Advanced: full router settings in LuCI. LuCI asks for its own root '
  + 'login; your OpenMANET sign-in stays as it is. Use Back to return.';

// Signing out ends the OpenMANET session only. With the Advanced entry
// shown, the tooltip says LuCI's separate sign-in is not affected.
const SIGN_OUT_TITLE = 'Sign out of OpenMANET';
const SIGN_OUT_TITLE_LUCI = `${SIGN_OUT_TITLE} (LuCI keeps its own sign-in)`;

const OVERFLOW_TABS = [
  { to: '/blos',     label: 'BLOS',     icon: 'blos' },
  { to: '/settings', label: 'Settings', icon: 'settings' },
];

export default function Layout() {
  const { logout } = useAuth();
  const [collapsed, setCollapsed] = useState(false);
  // `<=`, not `<`: the CSS breakpoint is `max-width: 768px`, which is
  // inclusive, so at exactly 768 (iPad portrait) the stylesheets are already
  // in their mobile form. A strict `<` here rendered the desktop sidebar
  // shell around single-column mobile content at that one width.
  const [isMobile, setIsMobile] = useState(
    () => typeof window !== 'undefined' && window.innerWidth <= MOBILE_BREAKPOINT
  );
  const [sheetOpen, setSheetOpen] = useState(false);
  const luciEnabled = useLuciProxy();
  const signOutTitle = luciEnabled ? SIGN_OUT_TITLE_LUCI : SIGN_OUT_TITLE;

  useEffect(() => {
    let timeoutId = null;
    const onResize = () => {
      if (timeoutId != null) return;
      timeoutId = setTimeout(() => {
        timeoutId = null;
        setIsMobile(window.innerWidth <= MOBILE_BREAKPOINT);
      }, 100);
    };
    window.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('resize', onResize);
      if (timeoutId != null) clearTimeout(timeoutId);
    };
  }, []);

  // The shell owns the body's layout reset. This was a `body:has(.layout-*)`
  // rule in Layout.css, but :has() is unsupported on the stock browsers of
  // some field devices and cannot be lowered by the build target, so the rule
  // was silently discarded there. A class is supported everywhere.
  useEffect(() => {
    document.body.classList.add('lat-shell-active');
    return () => document.body.classList.remove('lat-shell-active');
  }, []);

  // Mobile: bottom tab bar
  if (isMobile) {
    return (
      <div className="layout-mobile">
        <div className="layout-content">
          <SetupDismissBanner />
          <Outlet />
        </div>
        {sheetOpen && (
          <>
            <div className="tab-sheet-scrim" onClick={() => setSheetOpen(false)} />
            <nav className="tab-sheet" role="menu">
              {OVERFLOW_TABS.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  className={({ isActive }) => 'tab-sheet-item' + (isActive ? ' active' : '')}
                  onClick={() => setSheetOpen(false)}
                >
                  <span className="nav-icon"><NavIcon name={item.icon} /></span>
                  <span>{item.label}</span>
                </NavLink>
              ))}
              {luciEnabled ? (
                <a className="tab-sheet-item" href={LUCI_PATH} title={ADVANCED_TITLE}>
                  <span className="nav-icon"><NavIcon name="advanced" /></span>
                  <span className="nav-text">
                    <span>{ADVANCED_LABEL}</span>
                    <span className="nav-desc">{ADVANCED_DESC}</span>
                  </span>
                </a>
              ) : null}
              <button
                className="tab-sheet-item danger"
                onClick={() => { setSheetOpen(false); logout(); }}
                title={signOutTitle}
                type="button"
              >
                <span className="nav-icon"><NavIcon name="signout" /></span>
                <span>Sign Out</span>
              </button>
            </nav>
          </>
        )}
        <nav className="bottom-tab-bar">
          {PRIMARY_TABS.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              className={({ isActive }) => 'tab-item' + (isActive ? ' active' : '')}
            >
              <span className="tab-icon"><NavIcon name={item.icon} /></span>
              <span className="tab-label">{item.short}</span>
            </NavLink>
          ))}
          <button
            className={'tab-item' + (sheetOpen ? ' active' : '')}
            onClick={() => setSheetOpen((v) => !v)}
            type="button"
            aria-expanded={sheetOpen}
          >
            <span className="tab-icon"><NavIcon name="more" /></span>
            <span className="tab-label">More</span>
          </button>
        </nav>
      </div>
    );
  }

  // Desktop: collapsible sidebar
  return (
    <div className="layout-desktop">
      <nav className={'sidebar' + (collapsed ? ' collapsed' : '')}>
        <div className="sidebar-header">
          {!collapsed && (
            <div className="sidebar-brand">
              <div className="brand-title">◇ OpenMANET</div>
              <div className="brand-sub">Mesh Terminal</div>
            </div>
          )}
          <button
            className="sidebar-toggle"
            onClick={() => setCollapsed(!collapsed)}
            title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            type="button"
          >
            {collapsed ? '▶' : '◀'}
          </button>
        </div>
        <div className="sidebar-nav">
          {NAV_GROUPS.map((group) => (
            <React.Fragment key={group.label}>
              <div className="sidebar-group-label">{group.label}</div>
              {group.items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.to === '/'}
                  className={({ isActive }) => 'nav-item' + (isActive ? ' active' : '')}
                  title={item.label}
                >
                  <span className="nav-icon"><NavIcon name={item.icon} /></span>
                  {!collapsed && <span className="nav-label">{item.label}</span>}
                </NavLink>
              ))}
            </React.Fragment>
          ))}
          {luciEnabled ? (
            <a className="nav-item nav-item-handoff" href={LUCI_PATH} title={ADVANCED_TITLE}>
              <span className="nav-icon"><NavIcon name="advanced" /></span>
              {!collapsed && (
                <span className="nav-label nav-text">
                  <span>{ADVANCED_LABEL}</span>
                  <span className="nav-desc">{ADVANCED_DESC}</span>
                </span>
              )}
            </a>
          ) : null}
        </div>
        <div className="sidebar-footer">
          {!collapsed && <div className="operator">Operator</div>}
          <button className="sidebar-logout" onClick={logout} title={signOutTitle} type="button">
            {collapsed ? '×' : '× Sign Out'}
          </button>
        </div>
      </nav>
      <main className="layout-main">
        <SetupDismissBanner />
        <Outlet />
      </main>
    </div>
  );
}
