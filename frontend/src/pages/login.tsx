// Login — split-screen layout. Brand panel on the left, form on the right.
// Layout/styles live in globals.css (.login-shell); form logic is unchanged.

import { useState } from 'react';
import { useLogin } from '@refinedev/core';

import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';

type Values = { email: string; password: string };

export function LoginPage() {
  const { mutate: login, isPending } = useLogin<Values>();
  const [email, setEmail] = useState('admin@dcim.local');
  const [password, setPassword] = useState('changeme');
  const [emailErr, setEmailErr] = useState<string | undefined>();
  const [passwordErr, setPasswordErr] = useState<string | undefined>();

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const emailOk = email.length > 0 && /^.+@.+\..+$/.test(email);
    const passwordOk = password.length > 0;
    setEmailErr(emailOk ? undefined : 'Enter a valid email');
    setPasswordErr(passwordOk ? undefined : 'Password required');
    if (emailOk && passwordOk) login({ email, password });
  }

  return (
    <div className="login-shell">
      <aside className="login-brand" aria-hidden="true">
        <div className="login-wordmark">
          <span className="login-dot" />
          <span>USG DCIM</span>
        </div>
        <div>
          <h1 className="login-headline">Operate your fleet at every scale.</h1>
          <p className="login-sub">
            Unified inventory, capacity, and observability for enterprise
            data-center operations — from a single rack to a global footprint.
          </p>
        </div>
        <div className="login-meta">v0.2 · enterprise edition</div>
      </aside>

      <main className="login-form-panel">
        <div className="login-form-card">
          <h2 className="login-title">Welcome back</h2>
          <p className="login-subtitle">
            Sign in to continue. Production deployments use OIDC/SAML; local
            dev accepts the seeded admin.
          </p>

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

          <p className="login-footer-note">
            Trouble signing in? Contact your system administrator.
          </p>
        </div>
      </main>
    </div>
  );
}
