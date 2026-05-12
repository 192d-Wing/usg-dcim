// OIDC callback landing page. Keycloak redirects the browser here with
// ?code=... after a successful sign-in; we exchange the code for our
// own JWT via the backend, cache identity, then route into the app.

import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router';
import Box from '@cloudscape-design/components/box';
import Spinner from '@cloudscape-design/components/spinner';
import Alert from '@cloudscape-design/components/alert';

import { http, TOKEN_KEY } from '@/lib/http';

const IDENTITY_KEY = 'dcim.identity';
const ID_TOKEN_KEY = 'dcim.id_token';

export function LoginCallbackPage() {
  const [params] = useSearchParams();
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const code = params.get('code');
    const oidcError = params.get('error');
    const returnedState = params.get('state');

    // Always consume the sessionStorage values once we land here — even
    // on failure, leaving them around invites replay across tabs.
    const expectedState = sessionStorage.getItem('dcim.oidc.state');
    const expectedNonce = sessionStorage.getItem('dcim.oidc.nonce');
    sessionStorage.removeItem('dcim.oidc.state');
    sessionStorage.removeItem('dcim.oidc.nonce');

    if (oidcError) {
      setError(params.get('error_description') || oidcError);
      return;
    }
    if (!code) {
      setError('Missing authorization code in callback URL.');
      return;
    }
    // CSRF defense: the state the IdP echoed back must match the value
    // we minted before the authorize redirect. Mismatch means the user
    // landed here via a forged URL or the flow was tampered with.
    if (expectedState && returnedState !== expectedState) {
      setError('Sign-in state mismatch. Please retry.');
      return;
    }

    // The redirect_uri sent here must match the one the backend used
    // when starting the flow — Keycloak validates them byte-for-byte.
    const redirectUri = `${globalThis.location.origin}/login/callback`;

    (async () => {
      try {
        const callbackParams: Record<string, string> = {
          code, redirect_uri: redirectUri,
        };
        // Backend verifies this against id_token.nonce — defense
        // against id_token substitution (replay from a different flow).
        if (expectedNonce) callbackParams.nonce = expectedNonce;
        const tokenResp = await http.get('/auth/oidc/callback', {
          params: callbackParams,
        });
        localStorage.setItem(TOKEN_KEY, tokenResp.data.access_token);
        // Stash the IdP id_token so authProvider.logout() can pass it
        // to /auth/oidc/logout as `id_token_hint` and terminate the
        // Keycloak session, not just our local one.
        if (tokenResp.data.id_token) {
          localStorage.setItem(ID_TOKEN_KEY, tokenResp.data.id_token);
        }
        const me = await http.get('/auth/me');
        localStorage.setItem(IDENTITY_KEY, JSON.stringify({
          id: me.data.user.id,
          email: me.data.user.email,
          capabilities: me.data.capabilities ?? [],
        }));
        // Full navigation (not react-router navigate) so Refine's
        // cached useIsAuthenticated query from the prior /login mount
        // can't poison the next route's auth check.
        globalThis.location.replace('/');
      } catch (err: unknown) {
        const message =
          err instanceof Error ? err.message : 'OIDC callback failed.';
        setError(message);
      }
    })();
  }, [params]);

  return (
    <Box padding="xxl" textAlign="center">
      {error ? (
        <Alert type="error" header="Sign-in failed">
          {error}
        </Alert>
      ) : (
        <>
          <Spinner size="large" />
          <Box variant="p" padding={{ top: 'm' }}>
            Completing sign-in…
          </Box>
        </>
      )}
    </Box>
  );
}
