"use client";

import type { FormEvent } from "react";
import type { Application } from "@/lib/api";

export function ApplicationEditForm({ application, saving, onSave, onCancel }: {
  application: Application;
  saving: boolean;
  onSave: (event: FormEvent<HTMLFormElement>, id: string) => void;
  onCancel: () => void;
}) {
  // The key on the form (application.id) remounts it when a different
  // application is opened, so defaultValue fields never carry a previous
  // record's values.
  return (
    <aside className="capture-panel" aria-labelledby="edit-title" key={application.id}>
      <p className="eyebrow">Edit details</p>
      <h2 id="edit-title">Update application</h2>
      <p className="muted">Refine the details for this role. Changes save to this application only.</p>
      <form className="application-form" onSubmit={(event) => onSave(event, application.id)}>
        <label htmlFor="edit-company">Company</label>
        <input id="edit-company" name="company" autoComplete="organization" maxLength={200} required defaultValue={application.company} />
        <label htmlFor="edit-role">Role</label>
        <input id="edit-role" name="role" maxLength={200} required defaultValue={application.role} />
        <label htmlFor="edit-status">Status</label>
        <select id="edit-status" name="status" defaultValue={application.status}>
          <option value="saved">Saved</option>
          <option value="applied">Applied</option>
        </select>
        <label htmlFor="edit-source">Source <span className="optional">optional</span></label>
        <input id="edit-source" name="source" maxLength={200} defaultValue={application.source} placeholder="e.g. LinkedIn, referral" />
        <label htmlFor="edit-address">Address used <span className="optional">optional</span></label>
        <input id="edit-address" name="address" maxLength={200} autoComplete="email" defaultValue={application.address} placeholder="e.g. me@example.com" />
        <button className="button primary" type="submit" disabled={saving}>
          {saving ? "Saving…" : "Save changes"}
        </button>
        <button className="button quiet" type="button" onClick={onCancel} disabled={saving}>
          Cancel
        </button>
      </form>
    </aside>
  );
}