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

export default function Footer({
  brand,
  columns,
  copyright,
  socials,
}: FooterProps) {
  return (
    <footer className="border-t border-gray-200 bg-white">
      <div className="mx-auto max-w-7xl px-4 py-12 sm:px-6 lg:px-8">
        <div className="grid grid-cols-2 gap-8 md:grid-cols-4 lg:gap-12">
          {/* Brand column */}
          {brand && (
            <div className="col-span-2 md:col-span-1">
              <div className="text-lg font-bold text-gray-900">{brand}</div>
            </div>
          )}

          {/* Link columns */}
          {columns.map((col) => (
            <div key={col.title}>
              <h4 className="text-sm font-semibold text-gray-900">
                {col.title}
              </h4>
              <ul className="mt-4 space-y-3">
                {col.links.map((link) => (
                  <li key={link.label}>
                    <a
                      href={link.href}
                      className="text-sm text-gray-500 transition hover:text-gray-700"
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
        <div className="mt-12 flex flex-col items-center justify-between gap-4 border-t border-gray-100 pt-8 sm:flex-row">
          {copyright && (
            <p className="text-sm text-gray-400">{copyright}</p>
          )}
          {socials && socials.length > 0 && (
            <div className="flex gap-4">
              {socials.map((social, i) => (
                <a
                  key={i}
                  href={social.href}
                  className="text-gray-400 transition hover:text-gray-600"
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
