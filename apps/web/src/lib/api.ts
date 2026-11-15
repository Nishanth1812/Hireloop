export type Application = {
  id: string;
  company: string;
  role: string;
  status: "saved" | "applied";
};

export type ApplicationInput = Pick<Application, "company" | "role" | "status">;
export type ApplicationPatch = Partial<ApplicationInput>;

async function request<T>(path: string, token: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      ...init.headers,
      Authorization: `Bearer ${token}`,
    },
  });

  if (response.status === 401) throw new Error("Your session has expired. Sign out and sign in again.");
  if (response.status === 503) throw new Error("The API or database is unavailable. Check that the API is running and configured.");
  if (!response.ok) throw new Error(`Could not complete the request (${response.status}).`);
  if (response.status === 204) return undefined as T;
  return response.json();
}

export function listApplications(token: string): Promise<Application[]> {
  return request("/api/applications", token);
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
