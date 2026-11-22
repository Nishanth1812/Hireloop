"use client";

import type { FormEvent } from "react";
import { STAGE_CHOICES, stageLabel, type Application } from "@/lib/api";

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
      <p className="muted">Refine the details for this role. Clear a field to remove what you recorded.</p>
      <form className="application-form" onSubmit={(event) => onSave(event, application.id)}>
        <label htmlFor="edit-company">Company</label>
        <input id="edit-company" name="company" autoComplete="organization" maxLength={200} required defaultValue={application.company} />
        <label htmlFor="edit-role">Role</label>
        <input id="edit-role" name="role" maxLength={200} required defaultValue={application.role} />
        <label htmlFor="edit-status">Stage</label>
        <select id="edit-status" name="status" defaultValue={application.status}>
          {STAGE_CHOICES.map((status) => <option key={status} value={status}>{stageLabel(status)}</option>)}
        </select>
        <label htmlFor="edit-url">Job posting URL <span className="optional">optional</span></label>
        <input id="edit-url" name="url" type="url" maxLength={2048} defaultValue={application.url} placeholder="https://jobs.example.com/42" />
        <label htmlFor="edit-source">Source <span className="optional">optional</span></label>
        <input id="edit-source" name="source" maxLength={200} defaultValue={application.source} placeholder="e.g. LinkedIn, referral" />
        <label htmlFor="edit-channel">Channel <span className="optional">optional</span></label>
        <select id="edit-channel" name="channel" defaultValue={application.channel}>
          <option value="">Not recorded</option>
          <option value="campus">Campus</option>
          <option value="off-campus">Off-campus</option>
        </select>
        <label htmlFor="edit-job_id">Job ID <span className="optional">optional</span></label>
        <input id="edit-job_id" name="job_id" maxLength={200} defaultValue={application.job_id} placeholder="e.g. REQ-42" />
        <label htmlFor="edit-applied_on">Date applied <span className="optional">optional</span></label>
        <input id="edit-applied_on" name="applied_on" type="date" defaultValue={application.applied_on} />
        <label htmlFor="edit-address">Address used <span className="optional">optional</span></label>
        <input id="edit-address" name="address" maxLength={200} autoComplete="email" defaultValue={application.address} placeholder="e.g. me@example.com" />
        <label htmlFor="edit-notes">Notes <span className="optional">optional</span></label>
        <textarea id="edit-notes" name="notes" rows={3} maxLength={2000} defaultValue={application.notes} placeholder="Anything worth remembering about this application." />
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