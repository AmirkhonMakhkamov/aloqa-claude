import Link from "next/link";
import type { ReactNode } from "react";

/*
 * Shared frame for /login and /register. Keeps the branded column, the
 * centred card, and the footer link consistent so we only reason about
 * form layout inside each page.
 *
 * Design:
 *   - Light app background with a subtle radial accent wash in the corner
 *   - 420px centred card with accent-dim logo tile, Plus-Jakarta heading,
 *     ink-2 body, and a footer prompt linking to the opposite action
 */
export function AuthShell({
  title,
  subtitle,
  children,
  footerPrompt,
  footerHref,
  footerLabel,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
  footerPrompt: string;
  footerHref: string;
  footerLabel: string;
}) {
  return (
    <main className="relative flex min-h-full items-center justify-center overflow-hidden bg-app px-4 py-10">
      {/* Ambient accent wash — very subtle, so it reads as branded light
          rather than the hard navy gradient the old page used. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(1000px 500px at 85% -10%, color-mix(in oklab, var(--accent) 14%, transparent) 0%, transparent 60%), radial-gradient(800px 400px at -10% 110%, color-mix(in oklab, var(--accent) 10%, transparent) 0%, transparent 55%)",
        }}
      />
      <div className="relative w-full max-w-md space-y-8 rounded-2xl border border-line bg-app p-8 shadow-md">
        <header className="space-y-3 text-center">
          <div className="mx-auto grid h-11 w-11 place-items-center rounded-xl bg-accent text-base font-bold text-white shadow-sm">
            A
          </div>
          <div className="space-y-1">
            <h1 className="text-2xl font-semibold text-ink">{title}</h1>
            <p className="text-[13px] text-ink-2">{subtitle}</p>
          </div>
        </header>

        {children}

        <p className="text-center text-[13px] text-ink-2">
          {footerPrompt}{" "}
          <Link
            href={footerHref}
            className="font-medium text-accent hover:text-accent-hover hover:underline underline-offset-2"
          >
            {footerLabel}
          </Link>
        </p>
      </div>
    </main>
  );
}

// Red error card reused by both auth pages. Kept in the same file because
// it's a mini-primitive that only makes sense inside this shell.
export function AuthError({ message }: { message: string }) {
  return (
    <div
      role="alert"
      className="rounded-md border border-status-red/30 bg-status-red/5 p-3 text-[13px] text-status-red"
    >
      {message}
    </div>
  );
}
