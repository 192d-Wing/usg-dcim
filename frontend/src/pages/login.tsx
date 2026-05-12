// Login — split-screen layout. Brand panel on the left, form on the right.
// Layout/styles live in globals.css (.login-shell). All visible copy,
// colors, and the Cloudscape mode come from config/login-branding.ts.

import { useEffect, useState, type CSSProperties } from 'react';
import { useLogin } from '@refinedev/core';
import { applyMode, Mode } from '@cloudscape-design/global-styles';

import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';

import { loginBranding } from '@/config/login-branding';

type Values = { email: string; password: string };

// CSS custom properties consumed by .login-shell and descendants in
// globals.css. Typed as a CSSProperties extension so TS accepts the
// `--var` keys.
type BrandVars = CSSProperties & Record<`--login-${string}`, string>;

export function LoginPage() {
  const { mutate: login, isPending } = useLogin<Values>();
  const [email, setEmail] = useState('admin@dcim.local');
  const [password, setPassword] = useState('changeme');
  const [emailErr, setEmailErr] = useState<string | undefined>();
  const [passwordErr, setPasswordErr] = useState<string | undefined>();

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
    if (emailOk && passwordOk) login({ email, password });
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

          {loginBranding.sso.enabled && (
            <>
              <Button
                variant="primary"
                fullWidth
                onClick={() => {
                  globalThis.location.href = loginBranding.sso.loginUrl;
                }}
              >
                {loginBranding.sso.label}
              </Button>
              <div className="login-or-divider" role="separator">
                <span>or</span>
              </div>
            </>
          )}

          <form onSubmit={onSubmit}>
            <Form
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

          <p className="login-footer-note">{loginBranding.footerNote}</p>
        </div>
      </main>
    </div>
  );
}
