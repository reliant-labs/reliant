import React, { useState } from "react";

interface NavigationHeaderProps {
  brand: React.ReactNode;
  links: Array<{ label: string; href: string; active?: boolean }>;
  cta?: { label: string; href: string };
}

export default function NavigationHeader({ brand, links, cta }: NavigationHeaderProps) {
  const [menuOpen, setMenuOpen] = useState(false);

  return (
    <header className="border-b border-border bg-surface">
      <nav className="mx-auto flex max-w-7xl items-center justify-between px-4 py-3 sm:px-6 lg:px-8">
        {/* Brand */}
        <div className="text-lg font-bold text-ink">{brand}</div>

        {/* Desktop links */}
        <div className="hidden items-center gap-8 md:flex">
          {links.map((link) => (
            <a
              key={link.label}
              href={link.href}
              className={`text-sm font-medium transition ${
                link.active ? "text-accent" : "text-ink-muted hover:text-ink"
              }`}
            >
              {link.label}
            </a>
          ))}
          {cta && (
            <a
              href={cta.href}
              className="rounded-lg bg-accent px-4 py-2 text-sm font-semibold text-on-accent transition hover:bg-accent"
            >
              {cta.label}
            </a>
          )}
        </div>

        {/* Mobile hamburger */}
        <button
          type="button"
          className="inline-flex items-center justify-center rounded-md p-2 text-ink-muted hover:bg-surface-muted hover:text-ink-muted md:hidden"
          onClick={() => setMenuOpen(!menuOpen)}
          aria-expanded={menuOpen}
          aria-label="Toggle menu"
        >
          {menuOpen ? (
            <svg
              className="h-6 w-6"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth={1.5}
              stroke="currentColor"
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          ) : (
            <svg
              className="h-6 w-6"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth={1.5}
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="M3.75 6.75h16.5M3.75 12h16.5m-16.5 5.25h16.5"
              />
            </svg>
          )}
        </button>
      </nav>

      {/* Mobile menu */}
      {menuOpen && (
        <div className="border-t border-border px-4 pb-4 pt-2 md:hidden">
          <div className="flex flex-col gap-1">
            {links.map((link) => (
              <a
                key={link.label}
                href={link.href}
                className={`rounded-lg px-3 py-2 text-sm font-medium ${
                  link.active
                    ? "bg-accent-surface text-accent"
                    : "text-ink-muted hover:bg-surface-muted"
                }`}
              >
                {link.label}
              </a>
            ))}
            {cta && (
              <a
                href={cta.href}
                className="mt-2 rounded-lg bg-accent px-3 py-2 text-center text-sm font-semibold text-on-accent hover:bg-accent"
              >
                {cta.label}
              </a>
            )}
          </div>
        </div>
      )}
    </header>
  );
}
