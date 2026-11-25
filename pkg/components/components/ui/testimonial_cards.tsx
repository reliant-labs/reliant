interface TestimonialCardsProps {
  testimonials: Array<{
    quote: string;
    name: string;
    role: string;
    company?: string;
    rating?: number;
  }>;
}

function StarRating({ rating }: { rating: number }) {
  return (
    <div className="flex gap-0.5">
      {Array.from({ length: 5 }, (_, i) => (
        <svg
          key={i}
          className={`h-4 w-4 ${i < rating ? "text-warning" : "text-ink-subtle"}`}
          viewBox="0 0 20 20"
          fill="currentColor"
        >
          <path d="M9.049 2.927c.3-.921 1.603-.921 1.902 0l1.07 3.292a1 1 0 00.95.69h3.462c.969 0 1.371 1.24.588 1.81l-2.8 2.034a1 1 0 00-.364 1.118l1.07 3.292c.3.921-.755 1.688-1.54 1.118l-2.8-2.034a1 1 0 00-1.175 0l-2.8 2.034c-.784.57-1.838-.197-1.539-1.118l1.07-3.292a1 1 0 00-.364-1.118L2.98 8.72c-.783-.57-.38-1.81.588-1.81h3.461a1 1 0 00.951-.69l1.07-3.292z" />
        </svg>
      ))}
    </div>
  );
}

export default function TestimonialCards({ testimonials }: TestimonialCardsProps) {
  return (
    <div className="grid grid-cols-1 gap-6 md:grid-cols-2 lg:grid-cols-3">
      {testimonials.map((t) => (
        <div key={t.name} className="flex flex-col rounded-2xl border border-border bg-surface p-6">
          {t.rating != null && (
            <div className="mb-4">
              <StarRating rating={t.rating} />
            </div>
          )}

          <blockquote className="flex-1 text-ink-muted leading-relaxed">
            &ldquo;{t.quote}&rdquo;
          </blockquote>

          <div className="mt-6 flex items-center gap-3 border-t border-border pt-4">
            <div className="flex h-10 w-10 items-center justify-center rounded-full bg-surface-muted text-sm font-semibold text-ink-muted">
              {t.name
                .split(" ")
                .map((n) => n[0])
                .join("")
                .slice(0, 2)}
            </div>
            <div>
              <p className="text-sm font-semibold text-ink">{t.name}</p>
              <p className="text-xs text-ink-muted">
                {t.role}
                {t.company && <>, {t.company}</>}
              </p>
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}
