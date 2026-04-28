import { forwardRef, type InputHTMLAttributes } from "react";
import { cn } from "@/lib/utils";

/*
 * Light-palette input matching the Aloqa reference: app-2 fill on app surface
 * with a line border, accent focus ring, and a slightly larger touch target
 * (40px) than the old navy-dark variant. Placeholder uses ink-3 so it sits
 * above the eye-line of body copy without competing with real values.
 *
 * When `aria-invalid` is set (e.g. by form validation), we switch the border
 * + ring to the red status colour so the error state reads without needing
 * a separate inline message — the message layer stays optional.
 */
export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...rest }, ref) {
    return (
      <input
        ref={ref}
        className={cn(
          "h-10 w-full rounded-md border bg-app-2 px-3 text-sm text-ink placeholder:text-ink-3",
          "border-line outline-none transition-colors",
          "focus-visible:border-accent focus-visible:ring-2 focus-visible:ring-accent/20",
          "aria-[invalid=true]:border-status-red aria-[invalid=true]:focus-visible:ring-status-red/20",
          "disabled:cursor-not-allowed disabled:bg-app-3 disabled:text-ink-3",
          className,
        )}
        {...rest}
      />
    );
  },
);
