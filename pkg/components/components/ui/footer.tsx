import React from "react";

interface FooterProps {
  brand?: React.ReactNode;
  columns: Array<{
    title: string;
    links: Array<{ label: string; href: string }>;
  }>;
  copyright?: string;
  socials?: Array<{ icon: React.ReactNode; href: string }>;
}

export default function Footer({ brand, columns, copyright, socials }: FooterProps) {
  return (
    <footer className="border-t border-border bg-surface">
      <div className="mx-auto max-w-7xl px-4 py-12 sm:px-6 lg:px-8">
        <div className="grid grid-cols-2 gap-8 md:grid-cols-4 lg:gap-12">
          {/* Brand column */}
          {brand && (
            <div className="col-span-2 md:col-span-1">
              <div className="text-lg font-bold text-ink">{brand}</div>
            </div>
          )}

          {/* Link columns */}
          {columns.map((col) => (
            <div key={col.title}>
              <h4 className="text-sm font-semibold text-ink">{col.title}</h4>
              <ul className="mt-4 space-y-3">
                {col.links.map((link) => (
                  <li key={link.label}>
                    <a
                      href={link.href}
                      className="text-sm text-ink-muted transition hover:text-ink-muted"
                    >
                      {link.label}
                    </a>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>

        {/* Bottom bar */}
        <div className="mt-12 flex flex-col items-center justify-between gap-4 border-t border-border pt-8 sm:flex-row">
          {copyright && <p className="text-sm text-ink-subtle">{copyright}</p>}
          {socials && socials.length > 0 && (
            <div className="flex gap-4">
              {socials.map((social, i) => (
                <a
                  key={i}
                  href={social.href}
                  className="text-ink-subtle transition hover:text-ink-muted"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  {social.icon}
                </a>
              ))}
            </div>
          )}
        </div>
      </div>
    </footer>
  );
}
