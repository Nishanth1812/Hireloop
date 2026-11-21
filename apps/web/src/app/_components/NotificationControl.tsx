"use client";

type Props = {
  // null means the preference has not been confirmed by the API yet. It is
  // never treated as "off": the checkbox is disabled until a value arrives.
  enabled: boolean | null;
  loading: boolean;
  saving: boolean;
  error: string;
  notice: string;
  onChange: (enabled: boolean) => void;
};

export function NotificationControl({ enabled, loading, saving, error, notice, onChange }: Props) {
  const busy = loading || saving;
  const isUnknown = enabled === null;
  const preference = isUnknown
    ? "Your reminder preference is unknown."
    : enabled
      ? "Your reminder preference is on."
      : "Your reminder preference is off.";
  const status = loading
    ? "Loading notification settings…"
    : saving
      ? "Saving your preference…"
      : preference;
  // Disable the checkbox when the value is unknown (not yet loaded or failed to load)
  // or when a request is in flight.
  const disabled = busy || isUnknown;

  return (
    <section className="notification-control" aria-labelledby="notifications-title" aria-busy={busy}>
      <div className="notification-heading">
        <h2 id="notifications-title">Deadline reminders</h2>
        <p className="muted">Choose whether Hireloop may send you deadline reminders.</p>
      </div>

      <div className="notification-toggle">
        <input
          id="notifications-enabled"
          type="checkbox"
          checked={enabled ?? false}
          disabled={disabled}
          aria-describedby="notifications-state notifications-detail"
          onChange={(event) => {
            if (!disabled) onChange(event.target.checked);
          }}
        />
        <label htmlFor="notifications-enabled">Email deadline reminders</label>
      </div>

      <p id="notifications-state" className="notification-state" role="status" aria-live="polite">{status}</p>

      {error && <p className="message error-message" role="alert">{error}</p>}
      {notice && <p className="message success-message" role="status">{notice}</p>}

      <p id="notifications-detail" className="muted notification-detail">
        Turning this on only records your preference. It does not guarantee a reminder will be delivered,
        and it does not activate the reminder worker if one has not been configured. The default is
        reminders enabled; your choice overrides the default.
      </p>
    </section>
  );
}
