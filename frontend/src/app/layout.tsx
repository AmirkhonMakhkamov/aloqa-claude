import type { Metadata } from "next";
import { Plus_Jakarta_Sans } from "next/font/google";
import "./globals.css";
import { Providers } from "./providers";

/*
 * Aloqa's reference design uses Plus Jakarta Sans across the product.
 * next/font/google self-hosts the file and exposes it as a CSS variable
 * we then reference from tailwind.config.ts → fontFamily.sans.
 */
const jakarta = Plus_Jakarta_Sans({
  subsets: ["latin"],
  display: "swap",
  variable: "--font-sans",
  weight: ["400", "500", "600", "700", "800"],
});

export const metadata: Metadata = {
  title: "Aloqa",
  description: "Team communication — chat, calls, meetings.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`h-full ${jakarta.variable}`}>
      <body className="h-full bg-app font-sans text-ink antialiased">
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
