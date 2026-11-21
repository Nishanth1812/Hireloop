export type ApplicationStatus = "saved" | "applied";
export type ApplicationChannel = "campus" | "off-campus";

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
  Partial<Pick<Application, "source" | "address" | "url" | "channel" | "job_id" | "applied_on" | "notes">>;
export type ApplicationPatch = Partial<ApplicationInput>;

export type ApplicationFilters = {
  q: string;
  status: "" | ApplicationStatus;
  channel: "" | ApplicationChannel;
};

// ApplicationPage is the API's bounded keyset page. Field names mirror the
// wire format exactly, including snake_case. Total and Counts describe every
// matching row, so the UI never infers totals from a single page. next_cursor
// is an opaque position returned by the API, or null on the last page.
export type ApplicationPage = {
  items: Application[];
  page_size: number;
  total: number;
  counts: { saved: number; applied: number };
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

export type TaskStatus = "open" | "done" | "cancelled";
export type TaskPrecision = "none" | "date" | "instant";

// A task keeps its deadline precision on the wire. Date-only deadlines have a
// due_date and no time or zone; exact deadlines have a UTC due_at plus the
// original IANA time_zone and the wall time (due_local) that produced it.
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
  created_at: string;
};

export type TaskInput = {
  application_id: string;
  title: string;
  due_precision: TaskPrecision;
  due_date?: string;
  due_local?: string;
  time_zone?: string;
};

export type TaskPatch = Partial<Pick<Task, "title" | "status" | "due_precision" | "due_date" | "due_local" | "time_zone">>;

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