import { cn } from "@/lib/utils";

/*
 * Form row: label above control, optional hint or error below. Errors take
 * precedence over hints and share the status-red colour so the field glows
 * the same colour as its message.
 *
 * Labels are subtle (ink-2, uppercase, tracking-wide) so they don't compete
 * visually with the actual form values — matches the Aloqa auth + settings
 * pattern where the control is the visual anchor.
 */
interface Props {
  label: string;
  hint?: string;
  error?: string;
  children: React.ReactNode;
  className?: string;
  htmlFor?: string;
}

export function Field({ label, hint, error, children, className, htmlFor }: Props) {
  const Tag = htmlFor ? "div" : "label";
  return (
    <Tag className={cn("block space-y-1.5", className)}>
      {htmlFor ? (
        <label
          htmlFor={htmlFor}
          className="block text-[11px] font-semibold uppercase tracking-wider text-ink-2"
        >
          {label}
        </label>
      ) : (
        <span className="block text-[11px] font-semibold uppercase tracking-wider text-ink-2">
          {label}
        </span>
      )}
      {children}
      {error ? (
        <span className="block text-[12px] text-status-red">{error}</span>
      ) : hint ? (
        <span className="block text-[12px] text-ink-3">{hint}</span>
      ) : null}
    </Tag>
  );
}
