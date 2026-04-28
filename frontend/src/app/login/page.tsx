"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { AuthError, AuthShell } from "@/components/auth/AuthShell";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { useAuth } from "@/stores/auth";

/*
 * Sign-in flow. The useAuth store owns the actual login call — this page
 * is just the form + routing glue. Already-signed-in users bounce
 * straight to /w so a cached session doesn't strand them on the login
 * page after navigating back.
 */
export default function LoginPage() {
  const router = useRouter();
  const user = useAuth((s) => s.user);
  const login = useAuth((s) => s.login);

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (user) router.replace("/w");
  }, [user, router]);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await login(email, password);
      router.replace("/w");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Login failed");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <AuthShell
      title="Welcome back"
      subtitle="Sign in to continue to your workspace."
      footerPrompt="New to Aloqa?"
      footerHref="/register"
      footerLabel="Create an account"
    >
      <form className="space-y-4" onSubmit={onSubmit} noValidate>
        <Field label="Email" htmlFor="login-email">
          <Input
            id="login-email"
            type="email"
            autoComplete="email"
            autoFocus
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@company.com"
          />
        </Field>
        <Field label="Password" htmlFor="login-password">
          <Input
            id="login-password"
            type="password"
            autoComplete="current-password"
            required
            minLength={1}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="••••••••"
          />
        </Field>
        {error ? <AuthError message={error} /> : null}
        <Button
          type="submit"
          className="w-full"
          loading={submitting}
          disabled={submitting || !email || !password}
        >
          Sign in
        </Button>
      </form>
    </AuthShell>
  );
}
