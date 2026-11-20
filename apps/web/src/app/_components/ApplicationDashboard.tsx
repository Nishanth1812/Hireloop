"use client";

import { useEffect, useRef } from "react";
import type { FormEvent } from "react";
import Link from "next/link";
import { SignOutButton } from "@clerk/nextjs";
import type { Application, ApplicationFilters, ApplicationPage } from "@/lib/api";
import { ApplicationCaptureForm } from "./ApplicationCaptureForm";
import { ApplicationEditForm } from "./ApplicationEditForm";
import { ApplicationFilterBar } from "./ApplicationFilterBar";
import { ApplicationList } from "./ApplicationList";
import { ThemeControl } from "./ThemeControl";

type Props = {
  email: string;
  result: ApplicationPage;
  filters: ApplicationFilters;
  page: number;
  hasPrevious: boolean;
  hasNext: boolean;
  loading: boolean;
  loadError: boolean;
  saving: boolean;
  busyId: string;
  editingApplication: Application | null;
  error: string;
  notice: string;
  onCreate: (event: FormEvent<HTMLFormElement>) => void;
  onStatusChange: (application: Application, status: Application["status"]) => void;
  onEdit: (application: Application) => void;
  onCancelEdit: () => void;
  onSaveEdit: (event: FormEvent<HTMLFormElement>, id: string) => void;
  onRemove: (application: Application) => void;
  onApplyFilters: (filters: ApplicationFilters) => void;
  onClearFilters: () => void;
  onPrevious: () => void;
  onNext: () => void;
};

export function ApplicationDashboard({
  email, result, filters, page, hasPrevious, hasNext, loading, loadError, saving, busyId, editingApplication, error, notice,
  onCreate, onStatusChange, onEdit, onCancelEdit, onSaveEdit, onRemove, onApplyFilters, onClearFilters, onPrevious, onNext,
}: Props) {
  // Counts come from the API for every matching row, never from the current
  // page, so the summary stays honest while paging or filtering.
  const total = result.counts.saved + result.counts.applied;
  const lastEditingId = useRef("");
  const editingId = editingApplication?.id ?? "";

  useEffect(() => {
    if (editingId) {
      document.getElementById("edit-company")?.focus();
    } else if (lastEditingId.current) {
      document.getElementById(`edit-button-${lastEditingId.current}`)?.focus();
    }
    lastEditingId.current = editingId;
  }, [editingId]);

  return (
    <main className="page-shell">
      <header className="topbar">
        <Link className="wordmark" href="/" aria-label="Hireloop home"><span className="brand-mark">H</span>hireloop</Link>
        <div className="account-menu">
          <span className="account-email" title={email}>{email}</span>
          <ThemeControl />
          <SignOutButton><button className="button quiet" type="button">Sign out</button></SignOutButton>
        </div>
      </header>

      <section className="welcome-row">
        <div>
          <p className="eyebrow">Your job search</p>
          <h1>Applications</h1>
          <p className="muted">A calm place to keep track of every opportunity.</p>
        </div>
        <div className="summary" role="group" aria-label="Application counts">
          <div><strong>{total}</strong><span>Total</span></div>
          <div><strong>{result.counts.saved}</strong><span>Saved</span></div>
          <div><strong>{result.counts.applied}</strong><span>Applied</span></div>
        </div>
      </section>

      {error && <p className="message error-message" role="alert">{error}</p>}
      {notice && <p className="message success-message" role="status">{notice}</p>}

      <div className="workspace">
        <div className="list-column">
          <ApplicationFilterBar
            filters={filters}
            disabled={loading || saving}
            onApply={onApplyFilters}
            onClear={onClearFilters}
          />
          <ApplicationList
            result={result}
            filters={filters}
            page={page}
            hasPrevious={hasPrevious}
            hasNext={hasNext}
            loading={loading}
            loadError={loadError}
            busyId={busyId}
            saving={saving}
            editingId={editingApplication?.id ?? ""}
            onStatusChange={onStatusChange}
            onEdit={onEdit}
            onRemove={onRemove}
            onPrevious={onPrevious}
            onNext={onNext}
            onClearFilters={onClearFilters}
          />
        </div>
        <ApplicationCaptureForm saving={saving} hidden={!!editingApplication} onSubmit={onCreate} />
        {editingApplication && (
          <ApplicationEditForm
            application={editingApplication}
            saving={saving}
            onSave={onSaveEdit}
            onCancel={onCancelEdit}
          />
        )}
      </div>
      <footer className="footer-note">Hireloop keeps your job search organized, one next step at a time.</footer>
    </main>
  );
}