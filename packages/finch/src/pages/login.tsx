// Login — split-screen layout. Brand panel on the left, form on the right.
// Layout/styles live in globals.css (.login-shell). All visible copy,
// colors, and the Cloudscape mode come from config/login-branding.ts.
//
// Whether SSO is offered comes from the backend at runtime (GET
// /api/v1/auth/methods — sso is true exactly when the API has OIDC
// configured), not from a build-time constant, so environments without
// an IdP (e.g. the windep dev cluster) never render a dead E-ICAM
// button. Branding constants still control the button's label and the
// page's skin. When SSO is on, only the E-ICAM button is shown and the
// local email/password form is a break-glass fallback behind a
// disclosure link; while the methods call is in flight (or failed) the
// page falls back to local-form-visible / SSO-hidden.

import { useEffect, useState, type CSSProperties } from 'react';
import { useLogin } from '@refinedev/core';
import { applyMode, Mode } from '@cloudscape-design/global-styles';

import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';

import { loginBranding } from '@/config/login-branding';
import { http } from '@/lib/http';

type Values = { email: string; password: string };

/** GET /auth/methods response — see otter-go internal/auth/handler_methods.go. */
type AuthMethods = {
  local: boolean;
  sso: boolean;
  /** Navigation target for the SSO button; present only when sso is true. */
  sso_login_url?: string;
};

// CSS custom properties consumed by .login-shell and descendants in
// globals.css. Typed as a CSSProperties extension so TS accepts the
// `--var` keys.
type BrandVars = CSSProperties & Record<`--login-${string}`, string>;

function initiateOidc(loginUrl: string) {
  const randomB64 = (bytes = 16) => {
    const buf = new Uint8Array(bytes);
    globalThis.crypto.getRandomValues(buf);
    return btoa(String.fromCodePoint(...buf))
      .replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
  };
  const state = randomB64();
  const nonce = randomB64();
  sessionStorage.setItem('dcim.oidc.state', state);
  sessionStorage.setItem('dcim.oidc.nonce', nonce);
  const sep = loginUrl.includes('?') ? '&' : '?';
  globalThis.location.href =
    `${loginUrl}${sep}state=${state}&nonce=${nonce}`;
}

export function LoginPage() {
  const { mutate: login, isPending } = useLogin<Values>();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [emailErr, setEmailErr] = useState<string | undefined>();
  const [passwordErr, setPasswordErr] = useState<string | undefined>();
  const [formErr, setFormErr] = useState<string | undefined>();
  // Backend-reported auth methods. null until the fetch resolves — and
  // stays null when it fails — which the derived values below treat as
  // "no SSO": local form visible, no E-ICAM button. A hidden SSO button
  // during the (fast) fetch is harmless; a dead one never is.
  const [methods, setMethods] = useState<AuthMethods | null>(null);
  // The user's explicit show/hide choice via the disclosure link.
  // null = no choice yet → derive the default from SSO availability.
  const [localFormPref, setLocalFormPref] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    http
      .get<AuthMethods>('/auth/methods')
      .then(({ data }) => {
        if (!cancelled) setMethods(data);
      })
      .catch(() => {
        // Leave methods null — fallback already shows the local form.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Branding stays a kill-switch (a skin can opt out of the button
  // entirely), but it can no longer conjure a button the backend
  // can't honor.
  const ssoEnabled = loginBranding.sso.enabled && (methods?.sso ?? false);
  const ssoLoginUrl = methods?.sso_login_url ?? loginBranding.sso.loginUrl;
  // Local form is always visible when SSO is off; hidden by default when SSO is on.
  const showLocalForm = localFormPref ?? !ssoEnabled;

  // Force the configured Cloudscape mode while the login page is
  // mounted; restore the previous mode on unmount based on <html>.
  useEffect(() => {
    const wasDark = document.documentElement.classList.contains('dark');
    const desired = loginBranding.cloudscapeMode === 'dark' ? Mode.Dark : Mode.Light;
    applyMode(desired);
    return () => applyMode(wasDark ? Mode.Dark : Mode.Light);
  }, []);

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const emailOk = email.length > 0 && /^.+@.+\..+$/.test(email);
    const passwordOk = password.length > 0;
    setEmailErr(emailOk ? undefined : 'Enter a valid email');
    setPasswordErr(passwordOk ? undefined : 'Password required');
    setFormErr(undefined);
    if (emailOk && passwordOk) {
      // A rejected login RESOLVES with success:false (Refine surfaces
      // auth errors only via a notificationProvider, which this app
      // doesn't mount) — so failures must be read here or they render
      // nowhere and the form fails silently.
      login(
        { email, password },
        {
          onSuccess: (result) => {
            if (!result.success) {
              setFormErr(result.error?.message ?? 'Sign-in failed.');
            }
          },
        },
      );
    }
  }

  const c = loginBranding.colors;
  const styleVars: BrandVars = {
    '--login-primary': c.primary,
    '--login-brand-bg-start': c.brandBgStart,
    '--login-brand-bg-mid': c.brandBgMid,
    '--login-brand-bg-end': c.brandBgEnd,
    '--login-form-bg-start': c.formBgStart,
    '--login-form-bg-end': c.formBgEnd,
    '--login-text': c.text,
    '--login-form-heading': c.formHeading,
  };

  return (
    <div className="login-shell" style={styleVars}>
      <aside className="login-brand" aria-hidden="true">
        <div className="login-hero">
          <h1 className="login-product-name">{loginBranding.productName}</h1>
          {loginBranding.logo && (
            <img
              className="login-logo"
              src={loginBranding.logo.src}
              alt={loginBranding.logo.alt}
              style={{ height: loginBranding.logo.height ?? 160 }}
            />
          )}
          {loginBranding.headline && (
            <h2 className="login-headline">{loginBranding.headline}</h2>
          )}
          {loginBranding.tagline && (
            <p className="login-sub">{loginBranding.tagline}</p>
          )}
        </div>
        <div className="login-meta">{loginBranding.meta}</div>
      </aside>

      <main className="login-form-panel">
        <div className="login-form-card">
          <h2 className="login-title">{loginBranding.formTitle}</h2>
          <p className="login-subtitle">{loginBranding.formSubtitle}</p>

          {ssoEnabled && (
            <Button variant="primary" fullWidth onClick={() => initiateOidc(ssoLoginUrl)}>
              {loginBranding.sso.label}
            </Button>
          )}

          {showLocalForm && (
            <form onSubmit={onSubmit} style={ssoEnabled ? { marginTop: '1.5rem' } : undefined}>
              <Form
                errorText={formErr}
                actions={
                  <Button
                    variant="primary"
                    formAction="submit"
                    loading={isPending}
                    fullWidth
                  >
                    {isPending ? 'Signing in…' : 'Sign in'}
                  </Button>
                }
              >
                <SpaceBetween size="l">
                  <FormField label="Email" errorText={emailErr}>
                    <Input
                      type="email"
                      autoComplete="username"
                      placeholder="you@company.com"
                      value={email}
                      onChange={({ detail }) => setEmail(detail.value)}
                    />
                  </FormField>
                  <FormField label="Password" errorText={passwordErr}>
                    <Input
                      type="password"
                      autoComplete="current-password"
                      placeholder="••••••••"
                      value={password}
                      onChange={({ detail }) => setPassword(detail.value)}
                    />
                  </FormField>
                </SpaceBetween>
              </Form>
            </form>
          )}

          {ssoEnabled && (
            <p className="login-footer-note" style={{ marginTop: '1.25rem' }}>
              {showLocalForm ? (
                <>
                  {loginBranding.footerNote}{' '}
                  <button
                    type="button"
                    className="login-fallback-toggle"
                    onClick={() => setLocalFormPref(false)}
                  >
                    Hide local login
                  </button>
                </>
              ) : (
                <button
                  type="button"
                  className="login-fallback-toggle"
                  onClick={() => setLocalFormPref(true)}
                >
                  E-ICAM unavailable? Use local credentials
                </button>
              )}
            </p>
          )}

          {!ssoEnabled && (
            <p className="login-footer-note">{loginBranding.footerNote}</p>
          )}
        </div>
      </main>
    </div>
  );
}
