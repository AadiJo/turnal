import type { ComponentChildren } from "preact";
import { cx } from "./format";

/** The Turnal mark, inlined so the chrome needs no asset request. */
export function Glyph({ size = 20 }: { size?: number }) {
  return (
    <span className="glyph" style={{ width: size, height: size }} aria-hidden="true">
      <svg viewBox="0 0 256 256" fill="none">
        <path
          d="M50.7 141.6 A78.5 78.5 0 1 0 103.7 53.3 L100.9 66.9 A65.5 65.5 0 1 1 56.6 140.6 Z"
          fill="currentColor"
        />
        <path
          d="M75.7 68.7 L101.8 39.2 L114.2 77.2 Z"
          fill="currentColor"
          stroke="currentColor"
          strokeWidth="6"
          strokeLinejoin="round"
        />
        <circle cx="128" cy="128" r="21" fill="none" stroke="currentColor" strokeWidth="5" opacity="0.55" />
      </svg>
    </span>
  );
}

export type Crumb = { label: string; onClick?: () => void; mono?: boolean };

export function Chrome({
  crumbs,
  actions,
  onSearch,
}: {
  crumbs: Crumb[];
  actions?: ComponentChildren;
  onSearch?: () => void;
}) {
  return (
    <header className="chrome">
      <span className="mark">
        <Glyph /> Turnal
      </span>
      <nav className="crumbs">
        {crumbs.map((crumb, index) => (
          <>
            {index > 0 && <i>/</i>}
            {crumb.onClick ? (
              <a href="#" onClick={(event) => { event.preventDefault(); crumb.onClick?.(); }}>
                {crumb.label}
              </a>
            ) : (
              <b className={cx(crumb.mono && "tag mono")}>{crumb.label}</b>
            )}
          </>
        ))}
      </nav>
      <div className="chrome-right">
        {onSearch && (
          <button className="search" type="button" onClick={onSearch}>
            <span>Search projects, sessions, files</span>
            <span className="sp" />
            <kbd>⌘K</kbd>
          </button>
        )}
        <span className="chip">Read only</span>
        {actions}
      </div>
    </header>
  );
}

export type Tab = { id: string; label: string; count?: number };

export function Tabs({
  tabs,
  active,
  onSelect,
  meta,
}: {
  tabs: Tab[];
  active: string;
  onSelect: (id: string) => void;
  meta?: ComponentChildren;
}) {
  return (
    <nav className="tabs">
      {tabs.map((tab) => (
        <a
          key={tab.id}
          href="#"
          className={cx(active === tab.id && "on")}
          onClick={(event) => {
            event.preventDefault();
            onSelect(tab.id);
          }}
        >
          {tab.label}
          {tab.count !== undefined && <span className="count">{tab.count}</span>}
        </a>
      ))}
      <div className="tabs-right">{meta}</div>
    </nav>
  );
}

export function Section({
  title,
  note,
  children,
}: {
  title: string;
  note?: string;
  children?: ComponentChildren;
}) {
  return (
    <div className="sec">
      <h2>{title}</h2>
      {note && <span className="n">{note}</span>}
      <span className="sp" />
      {children}
    </div>
  );
}

export function Note({ children }: { children: ComponentChildren }) {
  return (
    <div className="note">
      <span className="badge">i</span>
      <span>{children}</span>
    </div>
  );
}

export function Delta({ additions, deletions }: { additions: number; deletions: number }) {
  return (
    <span className="delta">
      <span className="p">+{additions}</span>
      <span className="m">-{deletions}</span>
    </span>
  );
}
