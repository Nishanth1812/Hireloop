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

// ApplicationPage is the API's bounded list envelope. Total and Counts describe
// every matching row, so the UI never infers totals from a single page.
export type ApplicationPage = {
  items: Application[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
  counts: { saved: number; applied: number };
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

export function listApplications(token: string, filters: ApplicationFilters, page = 1): Promise<ApplicationPage> {
  const params = new URLSearchParams({ page: String(page) });
  if (filters.q.trim()) params.set("q", filters.q.trim());
  if (filters.status) params.set("status", filters.status);
  if (filters.channel) params.set("channel", filters.channel);
  return request(`/api/applications?${params.toString()}`, token);
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