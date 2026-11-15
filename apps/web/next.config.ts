import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  async rewrites() {
    const apiURL = (process.env.HIRELOOP_API_URL ?? "http://127.0.0.1:8080").replace(/\/+$/, "");
    return [{ source: "/api/:path*", destination: `${apiURL}/api/:path*` }];
  },
};

export default nextConfig;
