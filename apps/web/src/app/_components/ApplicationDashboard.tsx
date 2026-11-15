"use client";

import type { FormEvent } from "react";
import Link from "next/link";
import type { Session } from "@supabase/supabase-js";
import type { Application } from "@/lib/api";
import { ApplicationCaptureForm } from "./ApplicationCaptureForm";
import { ApplicationList } from "./ApplicationList";

type Props = {
  session: Session;
  applications: Application[];
  loading: boolean;
  loadError: boolean;
  saving: boolean;
  busyId: string;
  error: string;
  notice: string;
  onSignOut: () => void;
  onCreate: (event: FormEvent<HTMLFormElement>) => void;
  onStatusChange: (application: Application, status: Application["status"]) => void;
  onRemove: (application: Application) => void;
};

export function ApplicationDashboard({
  session, applications, loading, loadError, saving, busyId, error, notice,
  onSignOut, onCreate, onStatusChange, onRemove,
}: Props) {
  const savedCount = applications.filter((item) => item.status === "saved").length;
  const appliedCount = applications.length - savedCount;

  return (
    <main className="page-shell">
      <header className="topbar">
        <Link className="wordmark" href="/" aria-label="Hireloop home"><span className="brand-mark">H</span>hireloop</Link>
        <div className="account-menu">
          <span className="account-email">{session.user.email}</span>
          <button className="button quiet" onClick={onSignOut}>Sign out</button>
        </div>
      </header>

      <section className="welcome-row">
        <div>
          <p className="eyebrow">Your job search</p>
          <h1>Applications</h1>
          <p className="muted">A calm place to keep track of every opportunity.</p>
        </div>
        <div className="summary" role="group" aria-label="Application counts">
          <div><strong>{applications.length}</strong><span>Total</span></div>
          <div><strong>{savedCount}</strong><span>Saved</span></div>
          <div><strong>{appliedCount}</strong><span>Applied</span></div>
        </div>
      </section>

      {error && <p className="message error-message" role="alert">{error}</p>}
      {notice && <p className="message success-message" role="status">{notice}</p>}

      <div className="workspace">
        <ApplicationList
          applications={applications}
          loading={loading}
          loadError={loadError}
          busyId={busyId}
          onStatusChange={onStatusChange}
          onRemove={onRemove}
        />
        <ApplicationCaptureForm saving={saving} onSubmit={onCreate} />
      </div>
      <footer className="footer-note">Hireloop keeps your job search organized, one next step at a time.</footer>
    </main>
  );
}
