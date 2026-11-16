"use client";

import Link from "next/link";
import { ThemeControl } from "./ThemeControl";

export function SignInPanel() {
  return (
    <main className="page-shell auth-shell">
      <Link className="wordmark" href="/" aria-label="Hireloop home"><span className="brand-mark">H</span>hireloop</Link>
      <ThemeControl />
      <section className="sign-in-panel" aria-labelledby="welcome-title">
        <p className="eyebrow">A clearer job search</p>
        <h1 id="welcome-title">Keep every application moving.</h1>
        <p className="muted">Track where you applied and what needs your attention, all in one place.</p>
        <Link className="button primary sign-in-button" href="/sign-in">Sign in</Link>
        <p className="fine-print">Sign in securely. Hireloop never reads your inbox or messages.</p>
      </section>
      <p className="footer-note">Built for a more focused job search.</p>
    </main>
  );
}
