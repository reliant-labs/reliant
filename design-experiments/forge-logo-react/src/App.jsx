import { useId } from 'react';

const colorTokens = [
  { name: 'Carbon', value: '#0A0F14', role: 'Icon field and serious tool surface' },
  { name: 'Forged Steel', value: '#E7EDF2', role: 'Primary structure, readable at small sizes' },
  { name: 'Tempered Edge', value: '#8494A3', role: 'Depth on the support rails' },
  { name: 'Hot Pin', value: '#FF7A2F', role: 'Forge heat and active build state' },
  { name: 'Blueprint Blue', value: '#5FA8FF', role: 'Subtle construction-guide reference' },
];

function ForgeMark({ variant = 'full', title = 'Forge support-harness mark' }) {
  const uniqueId = useId().replace(/:/g, '');
  const titleId = `${variant}-${uniqueId}-title`;
  const steelGradientId = `${variant}-${uniqueId}-steel`;
  const heatGradientId = `${variant}-${uniqueId}-heat`;
  const shadowId = `${variant}-${uniqueId}-shadow`;
  const isMono = variant === 'mono';
  const isReverse = variant === 'reverse';

  return (
    <svg
      className={`forge-mark forge-mark--${variant}`}
      viewBox="0 0 256 256"
      role="img"
      aria-labelledby={titleId}
    >
      <title id={titleId}>{title}</title>
      <defs>
        <linearGradient id={steelGradientId} x1="48" y1="24" x2="208" y2="224" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor={isMono ? 'currentColor' : isReverse ? '#111820' : '#F4F7F9'} />
          <stop offset="0.55" stopColor={isMono ? 'currentColor' : isReverse ? '#17212B' : '#D8E1E8'} />
          <stop offset="1" stopColor={isMono ? 'currentColor' : isReverse ? '#273441' : '#8C9CAA'} />
        </linearGradient>
        <linearGradient id={heatGradientId} x1="112" y1="82" x2="151" y2="121" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#FFC27A" />
          <stop offset="0.48" stopColor="#FF7A2F" />
          <stop offset="1" stopColor="#C94E1D" />
        </linearGradient>
        <filter id={shadowId} x="25" y="25" width="206" height="212" colorInterpolationFilters="sRGB" filterUnits="userSpaceOnUse">
          <feDropShadow dx="0" dy="16" stdDeviation="14" floodColor="#02070C" floodOpacity="0.32" />
        </filter>
      </defs>

      {variant !== 'flat' && (
        <rect
          x="18"
          y="18"
          width="220"
          height="220"
          rx="54"
          fill={isReverse ? '#F4F7F9' : '#0A0F14'}
        />
      )}

      <g filter={variant === 'flat' ? undefined : `url(#${shadowId})`}>
        <path
          className="blueprint-guide blueprint-guide--primary"
          d="M68 64H196M68 118H174M68 176H146"
          stroke={isMono || isReverse ? 'currentColor' : '#5FA8FF'}
          strokeLinecap="round"
          strokeWidth="4"
          opacity={variant === 'full' ? '0.2' : '0'}
        />
        <path
          className="blueprint-guide"
          d="M91 43V184M128 72V185M166 72V139"
          stroke={isMono || isReverse ? 'currentColor' : '#5FA8FF'}
          strokeLinecap="round"
          strokeWidth="3"
          opacity={variant === 'full' ? '0.14' : '0'}
        />

        <path
          fill={`url(#${steelGradientId})`}
          d="M68 42c0-7.7 6.3-14 14-14h16c7.7 0 14 6.3 14 14v130h38.9c3.7 0 7.3 1.5 9.9 4.1l22.1 21.9H207c7.2 0 13 5.8 13 13v17H36v-17c0-7.2 5.8-13 13-13h24.1l22.1-21.9c2.6-2.6 6.2-4.1 9.9-4.1H68V42Z"
        />
        <path
          fill={`url(#${steelGradientId})`}
          d="M92 28h104c7.7 0 14 6.3 14 14v18c0 7.7-6.3 14-14 14H92V28Z"
        />
        <path
          fill={`url(#${steelGradientId})`}
          d="M92 98h76c7.7 0 14 6.3 14 14v16c0 7.7-6.3 14-14 14H92V98Z"
        />
        <path
          fill={isMono ? 'currentColor' : isReverse ? '#0A0F14' : '#101821'}
          opacity="0.28"
          d="M112 74h70c7.7 0 14-6.3 14-14V42c0-4-1.7-7.6-4.3-10.1 6.8.8 12.3 6.7 12.3 13.9V62c0 7.7-6.3 14-14 14h-78V74ZM112 142h56c7.7 0 14-6.3 14-14v-16c0-4-1.7-7.6-4.3-10.1 6.8.8 12.3 6.7 12.3 13.9V132c0 7.7-6.3 14-14 14h-64v-4ZM73 198h109.9l-22.1-21.9c-2.6-2.6-6.2-4.1-9.9-4.1H112v10h36.8c3.7 0 7.3 1.5 9.9 4.1l12 11.9H73Z"
        />
        <path
          fill={isMono ? 'currentColor' : `url(#${heatGradientId})`}
          d="M128 80 150 102 128 124 106 102 128 80Z"
        />
        <path
          fill={isMono ? 'currentColor' : '#FFE2BE'}
          fillOpacity={isMono ? '0.24' : '0.9'}
          d="M128 91 139 102 128 113 117 102 128 91Z"
        />
      </g>
    </svg>
  );
}

function Swatch({ token }) {
  return (
    <li className="swatch-row">
      <span className="swatch" style={{ backgroundColor: token.value }} />
      <span>
        <strong>{token.name}</strong>
        <small>{token.value} · {token.role}</small>
      </span>
    </li>
  );
}

export default function App() {
  return (
    <main className="page-shell">
      <section className="hero-panel" aria-labelledby="page-title">
        <div className="hero-copy">
          <p className="eyebrow">Forge logo experiment · ReactJS</p>
          <h1 id="page-title">A support harness for production-grade app generation.</h1>
          <p className="summary">
            The mark compresses a forge, scaffold, anvil, and blueprint into a single engineered
            frame: a structural F that holds the hot build pin instead of decorating around it.
          </p>
          <div className="logo-lockup" aria-label="Forge wordmark preview">
            <ForgeMark variant="flat" title="Forge mark without app tile" />
            <span>Forge</span>
          </div>
        </div>

        <div className="icon-stage" aria-label="Large app icon preview">
          <div className="blueprint-card">
            <ForgeMark variant="full" title="Forge app icon mark" />
          </div>
        </div>
      </section>

      <section className="detail-grid" aria-label="Logo design details">
        <article className="detail-card geometry-card">
          <h2>Geometry</h2>
          <p>
            The left rail, two beams, and anvil foot form a reinforced F. Open right-side negative
            space keeps the silhouette legible as a favicon while the base reads as launch-ready mass.
          </p>
          <div className="geometry-stack">
            <ForgeMark variant="flat" title="Forge mark geometry preview" />
            <div className="geometry-lines" aria-hidden="true">
              <span />
              <span />
              <span />
            </div>
          </div>
        </article>

        <article className="detail-card">
          <h2>Scale Tests</h2>
          <div className="scale-row" aria-label="Logo scale previews">
            <ForgeMark variant="full" title="Forge mark at 96 pixels" />
            <ForgeMark variant="full" title="Forge mark at 64 pixels" />
            <ForgeMark variant="full" title="Forge mark at 40 pixels" />
            <ForgeMark variant="mono" title="Monochrome Forge mark" />
          </div>
          <p>
            The hot center pin survives at small sizes as a single activation point; the rails remain
            readable in monochrome for product chrome and documentation.
          </p>
        </article>

        <article className="detail-card palette-card">
          <h2>Palette</h2>
          <ul className="swatch-list">
            {colorTokens.map((token) => (
              <Swatch key={token.name} token={token} />
            ))}
          </ul>
        </article>

        <article className="detail-card inverse-card">
          <h2>Production Readiness</h2>
          <div className="inverse-preview">
            <ForgeMark variant="reverse" title="Reverse Forge mark" />
            <div>
              <strong>App icon ready</strong>
              <p>
                A rounded carbon tile gives the mark reliable contrast in docks, launchers, and dark
                IDE sidebars without relying on fine detail.
              </p>
            </div>
          </div>
        </article>
      </section>
    </main>
  );
}