// Pretty-name the current browser+OS so the sessions list can say
// "Chrome on macOS" instead of "web". The backend stores whatever string we
// send in `device_info`; it's purely cosmetic, so heuristic UA sniffing is
// the right level of precision here (we don't need the full ua-parser-js).
//
// Runs on the browser only — on the server we return "web" so SSR doesn't
// produce a mismatched value vs. the first client render.

export function describeCurrentDevice(): string {
  if (typeof window === "undefined" || typeof navigator === "undefined") {
    return "web";
  }
  const ua = navigator.userAgent || "";
  const browser = detectBrowser(ua);
  const os = detectOS(ua);
  if (!browser && !os) return "web";
  if (!browser) return os;
  if (!os) return browser;
  return `${browser} on ${os}`;
}

function detectBrowser(ua: string): string {
  // Order matters: Edge/Opera identify as Chrome downstream, so match them
  // first. Brave doesn't expose a UA marker — it reads as Chrome and we're
  // fine with that.
  if (/Edg\//i.test(ua)) return "Edge";
  if (/OPR\/|Opera\//i.test(ua)) return "Opera";
  if (/Firefox\//i.test(ua)) return "Firefox";
  if (/Chrome\//i.test(ua)) return "Chrome";
  if (/Safari\//i.test(ua) && /Version\//i.test(ua)) return "Safari";
  return "";
}

function detectOS(ua: string): string {
  if (/Windows NT 10\.0/i.test(ua)) return "Windows 10/11";
  if (/Windows NT/i.test(ua)) return "Windows";
  if (/Mac OS X/i.test(ua)) return "macOS";
  if (/CrOS/i.test(ua)) return "ChromeOS";
  if (/Android/i.test(ua)) return "Android";
  if (/iPhone|iPad|iPod/i.test(ua)) return "iOS";
  if (/Linux/i.test(ua)) return "Linux";
  return "";
}
