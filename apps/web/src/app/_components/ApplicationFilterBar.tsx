"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import type { ApplicationFilters } from "@/lib/api";

// The draft state lets someone type a search without the list jumping on every
// keystroke. It is re-synced during render (not in an effect) when the applied
// filters change, so a select change keeps keyboard focus on the select.
export function ApplicationFilterBar({ filters, disabled, onApply, onClear }: {
  filters: ApplicationFilters;
  disabled: boolean;
  onApply: (filters: ApplicationFilters) => void;
  onClear: () => void;
}) {
  const [draft, setDraft] = useState<ApplicationFilters>(filters);
  const [synced, setSynced] = useState<ApplicationFilters>(filters);
  if (synced !== filters) {
    setSynced(filters);
    setDraft(filters);
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    onApply({ ...draft, q: draft.q.trim() });
  }

  function change(next: ApplicationFilters) {
    setDraft(next);
    onApply(next);
  }

  const hasDraft = draft.q !== "" || draft.status !== "" || draft.channel !== "";

  return (
    <form className="filter-bar" role="search" aria-labelledby="filter-title" onSubmit={submit}>
      <h2 className="visually-hidden" id="filter-title">Search and filter applications</h2>
      <div className="filter-field filter-search">
        <label htmlFor="filter-q">Search</label>
        <input
          id="filter-q"
          name="q"
          type="search"
          value={draft.q}
          maxLength={100}
          placeholder="Company, role or job ID"
          disabled={disabled}
          onChange={(event) => setDraft({ ...draft, q: event.target.value })}
        />
      </div>
      <div className="filter-field">
        <label htmlFor="filter-status">Status</label>
        <select
          id="filter-status"
          name="status"
          value={draft.status}
          disabled={disabled}
          onChange={(event) => change({ ...draft, status: event.target.value as ApplicationFilters["status"] })}
        >
          <option value="">All statuses</option>
          <option value="saved">Saved</option>
          <option value="applied">Applied</option>
        </select>
      </div>
      <div className="filter-field">
        <label htmlFor="filter-channel">Channel</label>
        <select
          id="filter-channel"
          name="channel"
          value={draft.channel}
          disabled={disabled}
          onChange={(event) => change({ ...draft, channel: event.target.value as ApplicationFilters["channel"] })}
        >
          <option value="">Any channel</option>
          <option value="campus">Campus</option>
          <option value="off-campus">Off-campus</option>
        </select>
      </div>
      <div className="filter-actions">
        <button className="button primary" type="submit" disabled={disabled}>Apply</button>
        <button className="button quiet" type="button" onClick={() => { setDraft({ q: "", status: "", channel: "" }); onClear(); }} disabled={disabled || (!hasDraft && filters.q === "" && filters.status === "" && filters.channel === "")}>
          Clear
        </button>
      </div>
    </form>
  );
}