import type { NextConfig } from "next";

const config: NextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  async rewrites() {
    const backend = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";
    return [
      { source: "/api/:path*", destination: `${backend}/api/:path*` },
      { source: "/files/:path*", destination: `${backend}/files/:path*` },
    ];
    // Note: /ws is not rewritten here. Next.js rewrites don't reliably proxy
    // the WebSocket upgrade handshake in dev, so the frontend should connect
    // directly to the backend via NEXT_PUBLIC_WS_URL=ws://localhost:8080/ws.
  },
  // typedRoutes is strict about dynamic href templates (e.g. `/w/${wsId}`).
  // Keeping it off so Link hrefs can be plain strings. Re-enable once every
  // link is emitted via a helper that returns Route-typed URLs.
};

export default config;
