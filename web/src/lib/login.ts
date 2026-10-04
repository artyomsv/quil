export interface FetchResponseLike {
  status: number;
  json(): Promise<unknown>;
}

export type FetchLike = (url: string, init: RequestInit) => Promise<FetchResponseLike>;

export interface LoginResult {
  key?: string;
  error?: string;
}

export const LOGIN_WRONG_CODE = 'Wrong code — check the quil web terminal';
export const LOGIN_BUSY = 'Too many logins at once, try again';
export const LOGIN_UNREACHABLE = 'Could not reach the quil web server';
export const LOGIN_BAD_ANSWER = 'The quil web server sent an answer this page does not understand';

// postLogin sends the code from the quil web terminal. Success is 200 with
// {"key": "..."}; the server also sets the session cookie. No attempt count is
// kept or shown: the server slows wrong codes down itself.
export async function postLogin(fetchFn: FetchLike, code: string): Promise<LoginResult> {
  let res: FetchResponseLike;
  try {
    res = await fetchFn('/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code }),
    });
  } catch {
    return { error: LOGIN_UNREACHABLE };
  }
  switch (res.status) {
    case 200: {
      let body: unknown;
      try {
        body = await res.json();
      } catch {
        return { error: LOGIN_BAD_ANSWER };
      }
      const key = (body as { key?: unknown } | null)?.key;
      if (typeof key === 'string' && key !== '') return { key };
      return { error: LOGIN_BAD_ANSWER };
    }
    case 401:
      return { error: LOGIN_WRONG_CODE };
    case 429:
      return { error: LOGIN_BUSY };
    default:
      return { error: `Login failed (HTTP ${res.status})` };
  }
}

// sessionGone is true only when the server answered that it does not know
// this browser's session (401). No answer, or any other one, is false: an
// unreachable server is not a reason to log out.
export async function sessionGone(fetchFn: FetchLike): Promise<boolean> {
  try {
    const res = await fetchFn('/session', { method: 'GET', credentials: 'same-origin' });
    return res.status === 401;
  } catch {
    return false;
  }
}

// hasSession asks whether the session cookie is still live on the server: 204
// yes, anything else (or no answer) no.
export async function hasSession(fetchFn: FetchLike): Promise<boolean> {
  try {
    const res = await fetchFn('/session', { method: 'GET', credentials: 'same-origin' });
    return res.status === 204;
  } catch {
    return false;
  }
}
