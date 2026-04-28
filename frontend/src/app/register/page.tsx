"use client";

import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { AuthError, AuthShell } from "@/components/auth/AuthShell";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { cn } from "@/lib/utils";
import { useAuth } from "@/stores/auth";

/*
 * Register flow. The backend enforces: ≥8 chars, at least one letter,
 * one digit, and one symbol. We mirror that in the UI as a live checklist
 * so a password that would be rejected server-side shows red locally —
 * users shouldn't have to round-trip to the server to learn they forgot a
 * digit.
 *
 * After a successful register the store auto-issues a login, then we
 * land at /onboarding so the user can pick "Create a workspace" or
 * "Continue as standalone" — the spec's two entry paths. Returning users
 * who hit /register accidentally bounce straight to /w.
 */
interface Rule {
  label: string;
  ok: (password: string) => boolean;
}

const RULES: Rule[] = [
  { label: "At least 8 characters", ok: (p) => p.length >= 8 },
  { label: "A letter", ok: (p) => /[A-Za-z]/.test(p) },
  { label: "A digit", ok: (p) => /\d/.test(p) },
  { label: "A symbol", ok: (p) => /[^A-Za-z0-9]/.test(p) },
];

export default function RegisterPage() {
  const router = useRouter();
  const user = useAuth((s) => s.user);
  const register = useAuth((s) => s.register);

  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // We intentionally DON'T auto-redirect on `user` being truthy. The
  // `register` action sets `user` synchronously after success, and an
  // auto-redirect to /w here would race with (and override) the
  // explicit `router.replace("/onboarding")` in onSubmit below — landing
  // the user on the workspace picker instead of the onboarding chooser.
  // Returning users who hit /register on purpose can use the footer
  // "Sign in" link.
  void user;

  const passwordOk = useMemo(() => RULES.every((r) => r.ok(password)), [password]);
  const canSubmit = !!name && !!email && passwordOk && !submitting;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    setError(null);
    setSubmitting(true);
    try {
      await register(email, password, name);
      router.replace("/onboarding");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not register");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Create your account"
      subtitle="Join your team in seconds."
      footerPrompt="Already have an account?"
      footerHref="/login"
      footerLabel="Sign in"
    >
      <form className="space-y-4" onSubmit={onSubmit} noValidate>
        <Field label="Name" htmlFor="reg-name">
          <Input
            id="reg-name"
            autoComplete="name"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Your full name"
          />
        </Field>
        <Field label="Email" htmlFor="reg-email">
          <Input
            id="reg-email"
            type="email"
            autoComplete="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@company.com"
          />
        </Field>
        <Field label="Password" htmlFor="reg-password">
          <Input
            id="reg-password"
            type="password"
            autoComplete="new-password"
            minLength={8}
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="At least 8 characters"
          />
        </Field>

        <ul className="grid grid-cols-2 gap-x-3 gap-y-1 pl-0.5 text-[12px]">
          {RULES.map((rule) => {
            const ok = rule.ok(password);
            return (
              <li
                key={rule.label}
                className={cn(
                  "flex items-center gap-1.5 transition-colors",
                  password.length === 0
                    ? "text-ink-3"
                    : ok
                      ? "text-status-green"
                      : "text-ink-3",
                )}
              >
                <span
                  aria-hidden
                  className={cn(
                    "grid h-3.5 w-3.5 place-items-center rounded-full text-[9px] leading-none",
                    ok
                      ? "bg-status-green text-white"
                      : "border border-line bg-app-2 text-transparent",
                  )}
                >
                  ✓
                </span>
                {rule.label}
              </li>
            );
          })}
        </ul>

        {error ? <AuthError message={error} /> : null}

        <Button
          type="submit"
          className="w-full"
          loading={submitting}
          disabled={!canSubmit}
        >
          Create account
        </Button>
      </form>
    </AuthShell>
  );
}
