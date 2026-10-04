import { describe, expect, it } from 'vitest';
import {
  type FetchLike,
  hasSession,
  LOGIN_BAD_ANSWER,
  LOGIN_BUSY,
  LOGIN_UNREACHABLE,
  LOGIN_WRONG_CODE,
  postLogin,
} from './login';

interface Call {
  url: string;
  init: RequestInit;
}

function fakeFetch(status: number, body?: unknown): { fetchFn: FetchLike; calls: Call[] } {
  const calls: Call[] = [];
  const fetchFn: FetchLike = async (url, init) => {
    calls.push({ url, init });
    return {
      status,
      json: async () => {
        if (body === undefined) throw new Error('no body');
        return body;
      },
    };
  };
  return { fetchFn, calls };
}

describe('postLogin', () => {
  it('posts the code as JSON to /login, same origin', async () => {
    const { fetchFn, calls } = fakeFetch(200, { key: 'k1' });
    await postLogin(fetchFn, 'ABCDE-FGHJK');
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toBe('/login');
    expect(calls[0]?.init.method).toBe('POST');
    expect(calls[0]?.init.credentials).toBe('same-origin');
    expect(calls[0]?.init.headers).toEqual({ 'Content-Type': 'application/json' });
    expect(JSON.parse(calls[0]?.init.body as string)).toEqual({ code: 'ABCDE-FGHJK' });
  });

  it('returns the key on 200', async () => {
    expect(await postLogin(fakeFetch(200, { key: 'k1' }).fetchFn, 'c')).toEqual({ key: 'k1' });
  });

  it('refuses a 200 without a usable key', async () => {
    for (const body of [undefined, null, {}, { key: '' }, { key: 7 }]) {
      expect(await postLogin(fakeFetch(200, body).fetchFn, 'c')).toEqual({ error: LOGIN_BAD_ANSWER });
    }
  });

  it('maps 401 and 429 to their messages', async () => {
    expect(await postLogin(fakeFetch(401).fetchFn, 'c')).toEqual({ error: LOGIN_WRONG_CODE });
    expect(await postLogin(fakeFetch(429).fetchFn, 'c')).toEqual({ error: LOGIN_BUSY });
  });

  it('gives a plain error for any other status', async () => {
    expect(await postLogin(fakeFetch(415).fetchFn, 'c')).toEqual({ error: 'Login failed (HTTP 415)' });
    expect(await postLogin(fakeFetch(204).fetchFn, 'c')).toEqual({ error: 'Login failed (HTTP 204)' });
  });

  it('says the server is unreachable when fetch throws', async () => {
    const fetchFn: FetchLike = async () => {
      throw new TypeError('network');
    };
    expect(await postLogin(fetchFn, 'c')).toEqual({ error: LOGIN_UNREACHABLE });
  });
});

describe('hasSession', () => {
  it('is true only for 204', async () => {
    const ok = fakeFetch(204);
    expect(await hasSession(ok.fetchFn)).toBe(true);
    expect(ok.calls[0]?.url).toBe('/session');
    expect(ok.calls[0]?.init.credentials).toBe('same-origin');
    expect(await hasSession(fakeFetch(401).fetchFn)).toBe(false);
    expect(await hasSession(fakeFetch(200).fetchFn)).toBe(false);
  });

  it('is false when fetch throws', async () => {
    const fetchFn: FetchLike = async () => {
      throw new TypeError('network');
    };
    expect(await hasSession(fetchFn)).toBe(false);
  });
});
