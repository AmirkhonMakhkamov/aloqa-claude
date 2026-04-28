import { avatarColor, cn, initials } from "@/lib/utils";

interface Props {
  name?: string;
  src?: string | null;
  size?: number;
  className?: string;
}

export function Avatar({ name, src, size = 32, className }: Props) {
  const style = { width: size, height: size, fontSize: Math.max(10, Math.round(size / 2.8)) };
  if (src) {
    return (
      <img
        src={src}
        alt={name ?? "avatar"}
        style={style}
        className={cn("rounded-md object-cover", className)}
      />
    );
  }
  const seed = name ?? "?";
  return (
    <div
      style={style}
      className={cn(
        "inline-flex select-none items-center justify-center rounded-md font-semibold text-white/90",
        avatarColor(seed),
        className,
      )}
    >
      {initials(name)}
    </div>
  );
}
