import type { AuthProvider } from '@refinedev/core';
import { http, TOKEN_KEY } from './http';

type Identity = {
  id: string | null;
  email: string | null;
  capabilities: string[];
};

const IDENTITY_KEY = 'dcim.identity';
const ID_TOKEN_KEY = 'dcim.id_token';

export const authProvider: AuthProvider = {
  async login({ email, password }: { email: string; password: string }) {
    try {
      const r = await http.post('/auth/login', { email, password });
      localStorage.setItem(TOKEN_KEY, r.data.access_token);
      const me = await http.get('/auth/me');
      const identity: Identity = {
        id: me.data.user.id,
        email: me.data.user.email,
        capabilities: me.data.capabilities ?? [],
      };
      localStorage.setItem(IDENTITY_KEY, JSON.stringify(identity));
      return { success: true, redirectTo: '/' };
    } catch (err: any) {
      return { success: false, error: { name: 'LoginError', message: err?.message ?? 'login failed' } };
    }
  },

  async logout() {
    // Capture the id_token before wiping local state, so we can pass
    // it to Keycloak's end-session endpoint and terminate the SSO
    // session — without this, the IdP cookie stays alive and the
    // next "Login using DOD E-ICAM" click silently re-signs the user in.
    const idToken = localStorage.getItem(ID_TOKEN_KEY);
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(IDENTITY_KEY);
    localStorage.removeItem(ID_TOKEN_KEY);
    if (idToken) {
      const redirect = encodeURIComponent(`${globalThis.location.origin}/login`);
      const hint = encodeURIComponent(idToken);
      globalThis.location.href =
        `/api/v1/auth/oidc/logout?id_token_hint=${hint}&post_logout_redirect_uri=${redirect}`;
      // The browser navigation above wins over any redirectTo Refine
      // tries to apply, so we just acknowledge success.
      return { success: true };
    }
    return { success: true, redirectTo: '/login' };
  },

  async check() {
    const token = localStorage.getItem(TOKEN_KEY);
    if (!token) return { authenticated: false, redirectTo: '/login', logout: false };
    try {
      const me = await http.get('/auth/me');
      const identity: Identity = {
        id: me.data.user.id,
        email: me.data.user.email,
        capabilities: me.data.capabilities ?? [],
      };
      localStorage.setItem(IDENTITY_KEY, JSON.stringify(identity));
      return { authenticated: true };
    } catch {
      return { authenticated: false, redirectTo: '/login', logout: true };
    }
  },

  async onError(error: any) {
    if (error?.statusCode === 401) {
      return { logout: true, redirectTo: '/login' };
    }
    return {};
  },

  async getPermissions() {
    const id = identityFromCache();
    return id?.capabilities ?? [];
  },

  async getIdentity() {
    return identityFromCache();
  },
};

function identityFromCache(): Identity | null {
  const raw = localStorage.getItem(IDENTITY_KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as Identity;
  } catch {
    return null;
  }
}
