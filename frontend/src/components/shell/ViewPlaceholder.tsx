import type { ComponentType } from "react";

/*
 * A consistent empty-state chrome for top-level nav destinations that
 * don't have their final UI yet (AI, Phone, Apps, Settings during the
 * phased rebuild). Keeps the shell from flashing a blank white panel.
 */
export function ViewPlaceholder({
  Icon,
  title,
  body,
}: {
  Icon: ComponentType<{ className?: string }>;
  title: string;
  body: string;
}) {
  return (
    <div className="flex h-full items-center justify-center px-10 text-center">
      <div className="max-w-md space-y-3">
        <div className="mx-auto grid h-14 w-14 place-items-center rounded-2xl bg-app-3 text-ink-3">
          <Icon className="h-7 w-7" />
        </div>
        <h1 className="text-xl font-semibold text-ink">{title}</h1>
        <p className="text-sm text-ink-2">{body}</p>
      </div>
    </div>
  );
}
