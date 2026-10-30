export class APIError extends Error {
  constructor(public status: number, message: string) { super(message); }
}
let csrf = '';
export function setCSRF(value: string) { csrf = value; }
export async function api<T>(path: string, method = 'GET', body?: unknown, revision?: number): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET') headers['X-CSRF-Token'] = csrf;
  if (revision !== undefined) headers['If-Match'] = `"${revision}"`;
  const response = await fetch(`/api/v1${path}`, {
    method, credentials: 'same-origin', headers,
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  const data = response.status === 204 ? undefined : await response.json().catch(() => ({error: `Unexpected server response (${response.status})`}));
  if (!response.ok) throw new APIError(response.status, data?.error || `Request failed (${response.status})`);
  return data as T;
}
