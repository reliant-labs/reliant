/**
 * Design Sandbox - Hub page that links to standalone HTML mockups.
 * Each mockup is a separate HTML file in /public/sandbox/
 */
export function DesignSandboxPage() {
  return (
    <iframe
      src="/sandbox/index.html"
      style={{
        width: '100vw',
        height: '100vh',
        border: 'none',
        position: 'fixed',
        top: 0,
        left: 0,
        zIndex: 9999,
      }}
      title="Design Sandbox"
    />
  );
}
