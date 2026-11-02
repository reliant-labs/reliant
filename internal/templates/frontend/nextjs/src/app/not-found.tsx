import Link from "next/link";

export default function NotFound() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center p-8">
      <div className="max-w-md text-center">
        <p className="mb-2 text-sm font-semibold uppercase tracking-wider text-accent">404</p>
        <h1 className="mb-4 text-3xl font-bold tracking-tight">Page not found</h1>
        <p className="mb-6 text-ink-muted">
          The page you&apos;re looking for doesn&apos;t exist or has been moved.
        </p>
        <Link
          href="/"
          className="rounded-md bg-accent px-4 py-2 text-sm font-medium text-on-accent hover:bg-accent-hover"
        >
          Return home
        </Link>
      </div>
    </main>
  );
}
