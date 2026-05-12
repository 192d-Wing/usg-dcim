// OIDC callback landing page. Keycloak redirects the browser here with
// ?code=... after a successful sign-in; we exchange the code for our
// own JWT via the backend, cache identity, then route into the app.

import { useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import Box from '@cloudscape-design/components/box';
import Spinner from '@cloudscape-design/components/spinner';
import Alert from '@cloudscape-design/components/alert';

import { http, TOKEN_KEY } from '@/lib/http';

const IDENTITY_KEY = 'dcim.identity';

export function LoginCallbackPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const code = params.get('code');
    const oidcError = params.get('error');
    if (oidcError) {
      setError(params.get('error_description') || oidcError);
      return;
    }
    if (!code) {
      setError('Missing authorization code in callback URL.');
      return;
    }

    // The redirect_uri sent here must match the one the backend used
    // when starting the flow — Keycloak validates them byte-for-byte.
    const redirectUri = `${globalThis.location.origin}/login/callback`;

    (async () => {
      try {
        const tokenResp = await http.get('/auth/oidc/callback', {
          params: { code, redirect_uri: redirectUri },
        });
        localStorage.setItem(TOKEN_KEY, tokenResp.data.access_token);
        const me = await http.get('/auth/me');
        localStorage.setItem(IDENTITY_KEY, JSON.stringify({
          id: me.data.user.id,
          email: me.data.user.email,
          capabilities: me.data.capabilities ?? [],
        }));
        navigate('/', { replace: true });
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : String(err);
        setError(message || 'OIDC callback failed.');
      }
    })();
  }, [params, navigate]);

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
