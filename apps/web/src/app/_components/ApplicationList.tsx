"use client";

import type { Application, ApplicationFilters, ApplicationPage } from "@/lib/api";

function formatDate(value: string) {
  // applied_on is date-only. Render it in UTC so no timezone can shift the day
  // or invent a time the candidate never gave.
  const [year, month, day] = value.split("-").map(Number);
  if (!year || !month || !day) return value;
  const date = new Date(Date.UTC(year, month - 1, day));
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}

export function ApplicationList({ result, filters, page, hasPrevious, hasNext, loading, loadError, busyId, saving, editingId, onStatusChange, onEdit, onRemove, onPrevious, onNext, onClearFilters }: {
  result: ApplicationPage;
  filters: ApplicationFilters;
  page: number;
  hasPrevious: boolean;
  hasNext: boolean;
  loading: boolean;
  loadError: boolean;
  busyId: string;
  saving?: boolean;
  editingId: string;
  onStatusChange: (application: Application, status: Application["status"]) => void;
  onEdit: (application: Application) => void;
  onRemove: (application: Application) => void;
  onPrevious: () => void;
  onNext: () => void;
  onClearFilters: () => void;
}) {
  const applications = result.items;
  const hasFilters = filters.q !== "" || filters.status !== "" || filters.channel !== "";

  return (
    <section className="application-section" aria-labelledby="applications-title" aria-busy={loading}>
      <div className="section-heading">
        <div><p className="eyebrow">Your pipeline</p><h2 id="applications-title">All applications <span className="count">{result.total}</span></h2></div>
      </div>
      <p className="results-summary" role="status">
        {loading
          ? "Loading applications…"
          : loadError
            ? "Your applications could not be loaded."
            : result.total === 0
              ? hasFilters ? "No applications match these filters." : "No applications yet."
              : `Showing ${applications.length} on this page · ${result.total} total`}
      </p>
      {loading ? (
        <p className="loading">Loading applications…</p>
      ) : result.total === 0 && !loadError ? (
        <div className="empty-state" role="status">
          <span className="empty-icon" aria-hidden="true">↗</span>
          {hasFilters ? (
            <>
              <h3>Nothing matches just now.</h3>
              <p>Try a different search, or clear the filters to see every application.</p>
              <button className="button quiet" type="button" onClick={onClearFilters}>Clear filters</button>
            </>
          ) : (
            <>
              <h3>Your next opportunity starts here.</h3>
              <p>Add a role to keep its details close and your next step clear.</p>
            </>
          )}
        </div>
      ) : loadError ? (
        <p className="loading" role="status">Your applications could not be loaded. Try again after checking your connection.</p>
      ) : (
        <ul className="application-list">
          {applications.map((application) => (
            <li className="application-row" key={application.id}>
              <div className="company-initial" aria-hidden="true">{application.company.slice(0, 1).toUpperCase()}</div>
              <div className="application-copy">
                <strong>{application.role}</strong>
                <span>{application.company}</span>
                {application.channel && <span className="application-meta">{application.channel === "campus" ? "Campus drive" : "Off-campus"}</span>}
                {application.job_id && <span className="application-meta">Job ID: {application.job_id}</span>}
                {application.applied_on && <span className="application-meta">Applied: {formatDate(application.applied_on)}</span>}
                {application.source && <span className="application-meta">via {application.source}</span>}
                {application.address && <span className="application-meta">{application.address}</span>}
                {application.url && (
                  <span className="application-meta">
                    <a className="application-link" href={application.url} target="_blank" rel="noreferrer noopener" aria-label={`Job posting for ${application.role} at ${application.company}`}>Job posting</a>
                  </span>
                )}
                {application.notes && <span className="application-meta application-notes">{application.notes}</span>}
              </div>
              <label className="visually-hidden" htmlFor={`status-${application.id}`}>Status for {application.role} at {application.company}</label>
              <select
                id={`status-${application.id}`}
                className={`status-select ${application.status}`}
                value={application.status}
                disabled={saving || busyId === application.id || editingId === application.id}
                onChange={(event) => onStatusChange(application, event.target.value as Application["status"])}
              >
                <option value="saved">Saved</option>
                <option value="applied">Applied</option>
              </select>
              <button
                className="button edit-button"
                id={`edit-button-${application.id}`}
                disabled={saving || busyId === application.id}
                aria-pressed={editingId === application.id}
                onClick={() => onEdit(application)}
                aria-label={`Edit ${application.role} at ${application.company}`}
              >
                Edit
              </button>
              <button className="button delete-button" disabled={saving || busyId === application.id || editingId === application.id} onClick={() => onRemove(application)} aria-label={`Delete ${application.role} at ${application.company}`}>
                Delete
              </button>
            </li>
          ))}
        </ul>
      )}
      {!loading && !loadError && (hasPrevious || hasNext) && (
        <nav className="pagination" aria-label="Application pages">
          <button
            className="button quiet"
            type="button"
            onClick={onPrevious}
            disabled={saving || !hasPrevious}
          >
            Previous
          </button>
          <span className="page-indicator">Page {page}</span>
          <button
            className="button quiet"
            type="button"
            onClick={onNext}
            disabled={saving || !hasNext}
          >
            Next
          </button>
        </nav>
      )}
    </section>
  );
}
