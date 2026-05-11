// Login — single-card centered form. Uses Cloudscape primitives so
// there's no Tailwind dependency once we remove it.

import { useState } from 'react';
import { useLogin } from '@refinedev/core';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
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
    <Box padding="xxl" textAlign="center">
      <div style={{ display: 'inline-block', minWidth: 380, textAlign: 'left', marginTop: '12vh' }}>
        <Container
          header={
            <Header
              variant="h1"
              description="Sign in to continue. Production deployments use OIDC/SAML; local dev accepts the seeded admin."
            >
              USG DCIM
            </Header>
          }
        >
          <form onSubmit={onSubmit}>
            <Form
              actions={
                <Button variant="primary" formAction="submit" loading={isPending}>
                  {isPending ? 'Signing in…' : 'Sign in'}
                </Button>
              }
            >
              <SpaceBetween size="m">
                <FormField label="Email" errorText={emailErr}>
                  <Input
                    type="email"
                    autoComplete="username"
                    value={email}
                    onChange={({ detail }) => setEmail(detail.value)}
                  />
                </FormField>
                <FormField label="Password" errorText={passwordErr}>
                  <Input
                    type="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={({ detail }) => setPassword(detail.value)}
                  />
                </FormField>
              </SpaceBetween>
            </Form>
          </form>
        </Container>
      </div>
    </Box>
  );
}
