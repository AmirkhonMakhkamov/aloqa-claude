import { forwardRef, type ButtonHTMLAttributes } from "react";
import { cn } from "@/lib/utils";

/*
 * Buttons are the single most visible surface in the product, so we match the
 * Aloqa reference precisely:
 *  - primary  = accent pill, used for the one "main" action in a panel
 *  - ghost    = transparent, low-emphasis row action (hover = app-2)
 *  - outline  = line border on app, used for a neutral "cancel" or secondary
 *               action next to a primary
 *  - danger   = red pill for destructive confirms (revoke session, end call)
 *
 * The dark-on-light palette means we rely on text colour, not opacity, for
 * the disabled state — pure opacity on a white surface looks washed-out
 * rather than disabled.
 */
type Variant = "primary" | "ghost" | "outline" | "danger";
type Size = "sm" | "md" | "lg";

interface Props extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
}

const variants: Record<Variant, string> = {
  primary:
    "bg-accent text-white hover:bg-accent-hover active:bg-accent-hover/95 shadow-sm disabled:bg-accent/40 disabled:hover:bg-accent/40",
  ghost:
    "bg-transparent text-ink hover:bg-app-2 active:bg-app-3 disabled:text-ink-3",
  outline:
    "border border-line bg-app text-ink hover:bg-app-2 active:bg-app-3 disabled:text-ink-3",
  danger:
    "bg-status-red text-white hover:bg-status-red/90 active:bg-status-red/95 shadow-sm disabled:bg-status-red/40",
};

const sizes: Record<Size, string> = {
  sm: "h-8 px-3 text-[13px] rounded-md",
  md: "h-10 px-4 text-sm rounded-md",
  lg: "h-12 px-5 text-base rounded-lg",
};

export const Button = forwardRef<HTMLButtonElement, Props>(function Button(
  { className, variant = "primary", size = "md", loading, disabled, children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || loading}
      className={cn(
        "inline-flex items-center justify-center gap-2 font-medium transition-colors outline-none focus-visible:ring-2 focus-visible:ring-accent/50 disabled:cursor-not-allowed",
        variants[variant],
        sizes[size],
        className,
      )}
      {...rest}
    >
      {loading ? (
        <span
          className={cn(
            "h-3.5 w-3.5 animate-spin rounded-full border-2 border-t-transparent",
            variant === "primary" || variant === "danger"
              ? "border-white/60"
              : "border-ink-3",
          )}
        />
      ) : null}
      {children}
    </button>
  );
});
