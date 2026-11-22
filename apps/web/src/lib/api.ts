export type ApplicationStatus = "saved" | "applied" | "interviewing" | "offer" | "accepted" | "rejected" | "withdrawn";
export type ApplicationChannel = "campus" | "off-campus";

// The workflow's stage order, shared by the capture, edit and list selects.
// rejected/withdrawn are terminal outcomes available from any stage.
export const STAGE_CHOICES: ApplicationStatus[] = [
  "saved", "applied", "interviewing", "offer", "accepted", "rejected", "withdrawn",
];

export function stageLabel(status: ApplicationStatus): string {
  switch (status) {
    case "interviewing": return "Interviewing";
    case "offer": return "Offer";
    case "accepted": return "Accepted";
    case "rejected": return "Rejected";
    case "withdrawn": return "Withdrawn";
    default: return status.charAt(0).toUpperCase() + status.slice(1);
  }
}

export type Application = {
  id: string;
  company: string;
  role: string;
  status: ApplicationStatus;
  source: string;
  address: string;
  url: string;
  // "" means the candidate did not record a channel; the API only stores the
  // two agreed values.
  channel: "" | ApplicationChannel;
  // job_id and applied_on keep the API's field names so the client stays a
  // thin, literal view of the wire format. applied_on is date-only.
  job_id: string;
  applied_on: string;
  notes: string;
};

export type ApplicationInput = Pick<Application, "company" | "role" | "status"> &
  Partial<Pick<Application, "source" | "address" | "url" | "channel" | "job_id" | "applied_on" | "notes">> &
  // stage_note is optional and only meaningful with a status change: the API
  // rejects a note without one.
  { stage_note?: string };
export type ApplicationPatch = Partial<ApplicationInput>;

export type ApplicationFilters = {
  q: string;
  status: "" | ApplicationStatus;
  channel: "" | ApplicationChannel;
};

export type ApplicationCounts = {
  saved: number;
  applied: number;
  interviewing: number;
  offer: number;
  accepted: number;
  rejected: number;
  withdrawn: number;
};

// ApplicationPage is the API's bounded keyset page. Field names mirror the
// wire format exactly, including snake_case. Total and Counts describe every
// matching row, so the UI never infers totals from a single page. next_cursor
// is an opaque position returned by the API, or null on the last page.
export type ApplicationPage = {
  items: Application[];
  page_size: number;
  total: number;
  counts: ApplicationCounts;
  next_cursor: string | null;
};

async function request<T>(path: string, token: string, init: RequestInit = {}): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      ...init,
      headers: {
        ...init.headers,
        Authorization: `Bearer ${token}`,
      },
    });
  } catch {
    // A thrown fetch (offline, DNS/connection refused, CORS) is a transport
    // failure, not an application error. Surface the same honest message as a
    // 503 so the UI never shows a raw "Failed to fetch".
    throw new Error("The API or database is unavailable. Check that the API is running and configured.");
  }

  if (response.status === 400) throw new Error("The API rejected these values. Check the highlighted fields and try again.");
  if (response.status === 401) throw new Error("Your session has expired. Sign out and sign in again.");
  if (response.status === 503) throw new Error("The API or database is unavailable. Check that the API is running and configured.");
  if (!response.ok) throw new Error(`Could not complete the request (${response.status}).`);
  if (response.status === 204) return undefined as T;
  return response.json();
}

// toChannel narrows a form value to the two channels the API accepts; anything
// else is recorded as "not recorded" instead of being sent and rejected.
export function toChannel(value: string): Application["channel"] {
  return value === "campus" || value === "off-campus" ? value : "";
}

// listApplications fetches one keyset page. cursor is the API's opaque
// next_cursor; null (or omitted) requests the first page. Filters are sent
// together with the cursor so the API can reject a token minted for a
// different filter set instead of silently skipping rows.
export function listApplications(token: string, filters: ApplicationFilters, cursor: string | null = null): Promise<ApplicationPage> {
  const params = new URLSearchParams();
  if (cursor) params.set("cursor", cursor);
  if (filters.q.trim()) params.set("q", filters.q.trim());
  if (filters.status) params.set("status", filters.status);
  if (filters.channel) params.set("channel", filters.channel);
  const query = params.toString();
  return request(`/api/applications${query ? `?${query}` : ""}`, token);
}

export function createApplication(token: string, input: ApplicationInput): Promise<Application> {
  return request("/api/applications", token, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

export function patchApplication(token: string, id: string, input: ApplicationPatch): Promise<Application> {
  return request(`/api/applications/${encodeURIComponent(id)}`, token, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

export function deleteApplication(token: string, id: string): Promise<void> {
  return request(`/api/applications/${encodeURIComponent(id)}`, token, { method: "DELETE" });
}

// getApplication fetches one owner-scoped application by id for its detail
// page.
export function getApplication(token: string, id: string): Promise<Application> {
  return request(`/api/applications/${encodeURIComponent(id)}`, token);
}

// ApplicationEvent is one row of the application's owner-scoped stage
// history. The field names mirror the wire format; a "created" event starts
// the record and a "stage_changed" event follows each successful stage
// change. A stage change may carry a short note and from/to statuses; edits
// to other fields never create events.
export type ApplicationEvent = {
  id: string;
  application_id: string;
  event_type: "created" | "stage_changed";
  from_status: "" | ApplicationStatus;
  to_status: "" | ApplicationStatus;
  note: string;
  occurred_at: string;
};

// ApplicationEventPage mirrors the tasks page envelope: the API returns
// newest-first (occurred_at DESC, id DESC) with an opaque next_cursor, or
// null on the last page.
export type ApplicationEventPage = {
  items: ApplicationEvent[];
  page_size: number;
  next_cursor: string | null;
};

export function listEvents(token: string, id: string, cursor: string | null = null): Promise<ApplicationEventPage> {
  const params = new URLSearchParams();
  if (cursor) params.set("cursor", cursor);
  const query = params.toString();
  return request(`/api/applications/${encodeURIComponent(id)}/events${query ? `?${query}` : ""}`, token);
}

export type TaskStatus = "open" | "done" | "cancelled";
export type TaskPrecision = "none" | "date" | "instant";

// A task keeps its deadline precision on the wire. Date-only deadlines have a
// due_date and no time or zone; exact deadlines have a UTC due_at plus the
// original IANA time_zone and the wall time (due_local) that produced it.
// reminder_zone is separate from time_zone: it is the browser zone captured
// for approximate date-only reminders and is never a cutoff or a deadline.
export type Task = {
  id: string;
  application_id: string;
  title: string;
  status: TaskStatus;
  due_precision: TaskPrecision;
  due_date: string;
  due_local: string;
  due_at: string;
  time_zone: string;
  reminder_zone: string;
  created_at: string;
};

export type TaskInput = {
  application_id: string;
  title: string;
  due_precision: TaskPrecision;
  due_date?: string;
  due_local?: string;
  time_zone?: string;
  reminder_zone?: string;
};

export type TaskPatch = Partial<Pick<Task, "title" | "status" | "due_precision" | "due_date" | "due_local" | "time_zone" | "reminder_zone">>;

// TaskPage is the API's bounded keyset page. total counts every row matching
// the view (open only by default), so the Today panel never treats one page
// as the whole list. next_cursor is an opaque position or null on the last page.
export type TaskPage = {
  items: Task[];
  page_size: number;
  total: number;
  next_cursor: string | null;
};

// listTasks fetches one page. The cursor is the API's opaque next_cursor;
// null (or omitted) requests the first page. The view is sent with the cursor
// so the API can reject a token minted for a different filter set.
export function listTasks(token: string, showAll = false, cursor: string | null = null): Promise<TaskPage> {
  const params = new URLSearchParams();
  if (cursor) params.set("cursor", cursor);
  if (showAll) params.set("filter", "all");
  const query = params.toString();
  return request(`/api/tasks${query ? `?${query}` : ""}`, token);
}

// listTasksByApplication fetches every task linked to the application: the
// API verifies the application belongs to the caller before returning rows,
// and "all" includes done and cancelled actions, which a detail surface
// should show.
export function listTasksByApplication(token: string, applicationID: string, cursor: string | null = null): Promise<TaskPage> {
  const params = new URLSearchParams();
  params.set("application_id", applicationID);
  params.set("filter", "all");
  if (cursor) params.set("cursor", cursor);
  return request(`/api/tasks?${params.toString()}`, token);
}

export function createTask(token: string, input: TaskInput): Promise<Task> {
  return request("/api/tasks", token, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

export function patchTask(token: string, id: string, input: TaskPatch): Promise<Task> {
  return request(`/api/tasks/${encodeURIComponent(id)}`, token, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

// taskIcsUrl is the URL serving a task's .ics calendar export. The API route
// is owner-scoped and returns the calendar as text/calendar.
export function taskIcsUrl(id: string): string {
  return `/api/tasks/${encodeURIComponent(id)}/ics`;
}

// NotificationSettings is the API's owner-scoped preference envelope. The
// field name mirrors the wire format (snake_case); there is no delivery claim
// in it — it records the preference only.
export type NotificationSettings = {
  reminders_enabled: boolean;
};

export function getNotificationSettings(token: string): Promise<NotificationSettings> {
  return request("/api/notification-settings", token);
}

export function patchNotificationSettings(token: string, enabled: boolean): Promise<NotificationSettings> {
  return request("/api/notification-settings", token, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ reminders_enabled: enabled }),
  });
}
