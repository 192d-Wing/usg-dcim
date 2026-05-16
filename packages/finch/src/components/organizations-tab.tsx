// Organizations — the owning entity for ASNs and (eventually) other
// ARIN-tracked resources. Field shape mirrors ARIN's Org + POC
// templates so the same record can be copy-pasted into ARIN Online.

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';

type Organization = {
  id: string;
  name: string;
  arin_org_id: string | null;
  address_line1: string;
  address_line2: string | null;
  city: string;
  state_province: string | null;
  postal_code: string | null;
  country: string;
  phone: string | null;
  email: string | null;
  admin_poc_name: string;
  admin_poc_email: string;
  admin_poc_phone: string | null;
  tech_poc_name: string;
  tech_poc_email: string;
  tech_poc_phone: string | null;
  abuse_poc_name: string;
  abuse_poc_email: string;
  abuse_poc_phone: string | null;
  noc_poc_name: string | null;
  noc_poc_email: string | null;
  noc_poc_phone: string | null;
  description: string | null;
};

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const COUNTRY_RE = /^[A-Z]{2}$/;

export function OrganizationsTab({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient();
  const orgsQ = useQuery({
    queryKey: ['organizations'],
    queryFn: async () => (
      await http.get<{ items: Organization[] }>('/organizations?page_size=500')
    ).data.items ?? [],
  });
  const orgs = orgsQ.data ?? [];

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Organization | null>(null);

  async function remove(o: Organization) {
    if (!window.confirm(`Delete organization ${o.name}?`)) return;
    try {
      await http.delete(`/organizations/${o.id}`);
      toast.success('Organization removed');
      await qc.invalidateQueries({ queryKey: ['organizations'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<Organization>
        variant="container"
        loading={orgsQ.isLoading}
        loadingText="Loading organizations…"
        items={orgs}
        trackBy="id"
        header={
          <Header
            counter={`(${orgs.length})`}
            description="Owning entities for ASNs and other registered resources. Fields mirror ARIN's Org + POC templates."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New organization
              </Button>
            )}
          >
            Organizations
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (o) => o.name },
          {
            id: 'arin', header: 'ARIN OrgID',
            cell: (o) => o.arin_org_id
              ? <Badge color="blue">{o.arin_org_id}</Badge>
              : <Box color="text-status-inactive" fontSize="body-s">unregistered</Box>,
            width: 160,
          },
          {
            id: 'location', header: 'Location',
            cell: (o) => `${o.city}, ${o.state_province ? o.state_province + ' ' : ''}${o.country}`,
          },
          {
            id: 'admin', header: 'Admin POC',
            cell: (o) => (
              <Box fontSize="body-s">
                {o.admin_poc_name}
                <br />
                <Box variant="span" color="text-status-inactive">{o.admin_poc_email}</Box>
              </Box>
            ),
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (o: Organization) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(o)} ariaLabel={`Edit ${o.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(o)} ariaLabel={`Delete ${o.name}`} />
              </SpaceBetween>
            ),
            width: 90,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            <SpaceBetween size="xs">
              <b>No organizations yet</b>
              <Box variant="p" color="inherit">Add one to start tagging ASNs with an owner.</Box>
            </SpaceBetween>
          </Box>
        }
      />
      {canWrite && (
        <>
          <Modal
            visible={createOpen}
            onDismiss={() => setCreateOpen(false)}
            header="New organization"
            size="large"
          >
            <OrganizationForm
              onSaved={async () => {
                setCreateOpen(false);
                await qc.invalidateQueries({ queryKey: ['organizations'] });
              }}
            />
          </Modal>
          <Modal
            visible={editing !== null}
            onDismiss={() => setEditing(null)}
            header="Edit organization"
            size="large"
          >
            {editing && (
              <OrganizationForm
                org={editing}
                onSaved={async () => {
                  setEditing(null);
                  await qc.invalidateQueries({ queryKey: ['organizations'] });
                }}
              />
            )}
          </Modal>
        </>
      )}
    </>
  );
}

function OrganizationForm({
  org, onSaved,
}: Readonly<{ org?: Organization; onSaved: () => void }>) {
  const editing = !!org;
  const [name, setName] = useState(org?.name ?? '');
  const [arinOrgId, setArinOrgId] = useState(org?.arin_org_id ?? '');
  const [addressLine1, setAddressLine1] = useState(org?.address_line1 ?? '');
  const [addressLine2, setAddressLine2] = useState(org?.address_line2 ?? '');
  const [city, setCity] = useState(org?.city ?? '');
  const [stateProvince, setStateProvince] = useState(org?.state_province ?? '');
  const [postalCode, setPostalCode] = useState(org?.postal_code ?? '');
  const [country, setCountry] = useState(org?.country ?? 'US');
  const [phone, setPhone] = useState(org?.phone ?? '');
  const [email, setEmail] = useState(org?.email ?? '');
  const [adminName, setAdminName] = useState(org?.admin_poc_name ?? '');
  const [adminEmail, setAdminEmail] = useState(org?.admin_poc_email ?? '');
  const [adminPhone, setAdminPhone] = useState(org?.admin_poc_phone ?? '');
  const [techName, setTechName] = useState(org?.tech_poc_name ?? '');
  const [techEmail, setTechEmail] = useState(org?.tech_poc_email ?? '');
  const [techPhone, setTechPhone] = useState(org?.tech_poc_phone ?? '');
  const [abuseName, setAbuseName] = useState(org?.abuse_poc_name ?? '');
  const [abuseEmail, setAbuseEmail] = useState(org?.abuse_poc_email ?? '');
  const [abusePhone, setAbusePhone] = useState(org?.abuse_poc_phone ?? '');
  const [nocName, setNocName] = useState(org?.noc_poc_name ?? '');
  const [nocEmail, setNocEmail] = useState(org?.noc_poc_email ?? '');
  const [nocPhone, setNocPhone] = useState(org?.noc_poc_phone ?? '');
  const [description, setDescription] = useState(org?.description ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  // "Same as Admin POC" toggles. When true, the corresponding POC
  // fields mirror the admin values live and are disabled. Seed each
  // toggle from existing rows where the values already match — so
  // editing an org that was created with the checkbox keeps the
  // affordance visible.
  const [techSameAsAdmin, setTechSameAsAdmin] = useState(
    !!org
    && org.tech_poc_name === org.admin_poc_name
    && org.tech_poc_email === org.admin_poc_email
    && (org.tech_poc_phone ?? '') === (org.admin_poc_phone ?? ''),
  );
  const [abuseSameAsAdmin, setAbuseSameAsAdmin] = useState(
    !!org
    && org.abuse_poc_name === org.admin_poc_name
    && org.abuse_poc_email === org.admin_poc_email
    && (org.abuse_poc_phone ?? '') === (org.admin_poc_phone ?? ''),
  );
  const [nocSameAsAdmin, setNocSameAsAdmin] = useState(
    !!org
    && (org.noc_poc_name ?? '') === org.admin_poc_name
    && (org.noc_poc_email ?? '') === org.admin_poc_email
    && (org.noc_poc_phone ?? '') === (org.admin_poc_phone ?? ''),
  );

  // Effective POC values used on submit + when fields are disabled.
  // Mirroring lives at submit-time + via the field's `value=` so we
  // don't write back into the per-section state every keystroke.
  const effTechName = techSameAsAdmin ? adminName : techName;
  const effTechEmail = techSameAsAdmin ? adminEmail : techEmail;
  const effTechPhone = techSameAsAdmin ? adminPhone : techPhone;
  const effAbuseName = abuseSameAsAdmin ? adminName : abuseName;
  const effAbuseEmail = abuseSameAsAdmin ? adminEmail : abuseEmail;
  const effAbusePhone = abuseSameAsAdmin ? adminPhone : abusePhone;
  const effNocName = nocSameAsAdmin ? adminName : nocName;
  const effNocEmail = nocSameAsAdmin ? adminEmail : nocEmail;
  const effNocPhone = nocSameAsAdmin ? adminPhone : nocPhone;


  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (!addressLine1.trim()) errs.address_line1 = 'Required';
    if (!city.trim()) errs.city = 'Required';
    if (!country.trim()) errs.country = 'Required';
    else if (!COUNTRY_RE.test(country.toUpperCase())) {
      errs.country = '2-letter ISO code (e.g. US)';
    }
    // US/CA require state + postal.
    if (['US', 'CA'].includes(country.toUpperCase())) {
      if (!stateProvince.trim()) errs.state_province = 'Required for US/CA';
      if (!postalCode.trim()) errs.postal_code = 'Required for US/CA';
    }
    // POCs — name + email required for admin/tech/abuse. Validate the
    // *effective* values so a "same as Admin POC" section inherits the
    // admin's validation result.
    for (const [prefix, n, em] of [
      ['admin', adminName, adminEmail],
      ['tech', effTechName, effTechEmail],
      ['abuse', effAbuseName, effAbuseEmail],
    ] as const) {
      if (!n.trim()) errs[`${prefix}_poc_name`] = 'Required';
      if (!em.trim()) errs[`${prefix}_poc_email`] = 'Required';
      else if (!EMAIL_RE.test(em)) errs[`${prefix}_poc_email`] = 'Invalid email';
    }
    // Optional NOC: validate email only if provided.
    if (effNocEmail.trim() && !EMAIL_RE.test(effNocEmail)) errs.noc_poc_email = 'Invalid email';
    if (email.trim() && !EMAIL_RE.test(email)) errs.email = 'Invalid email';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;

    setSubmitting(true);
    const payload = {
      name,
      arin_org_id: arinOrgId || null,
      address_line1: addressLine1,
      address_line2: addressLine2 || null,
      city,
      state_province: stateProvince || null,
      postal_code: postalCode || null,
      country: country.toUpperCase(),
      phone: phone || null,
      email: email || null,
      admin_poc_name: adminName,
      admin_poc_email: adminEmail,
      admin_poc_phone: adminPhone || null,
      tech_poc_name: effTechName,
      tech_poc_email: effTechEmail,
      tech_poc_phone: effTechPhone || null,
      abuse_poc_name: effAbuseName,
      abuse_poc_email: effAbuseEmail,
      abuse_poc_phone: effAbusePhone || null,
      noc_poc_name: effNocName || null,
      noc_poc_email: effNocEmail || null,
      noc_poc_phone: effNocPhone || null,
      description: description || null,
    };
    try {
      if (editing && org) {
        await http.patch(`/organizations/${org.id}`, payload);
        toast.success('Organization updated');
      } else {
        await http.post('/organizations', payload);
        toast.success('Organization created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Saving…' : editing ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="l">
          <SpaceBetween size="m">
            <Box variant="awsui-key-label">Identity</Box>
            <ColumnLayout columns={2}>
              <FormField label="Legal name (OrgName)" errorText={errors.name}>
                <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. Example Corp" />
              </FormField>
              <FormField label="ARIN OrgID" description="Handle assigned by ARIN (e.g. EXAMPLE-1). Leave blank until registered.">
                <Input value={arinOrgId} onChange={({ detail }) => setArinOrgId(detail.value)} />
              </FormField>
            </ColumnLayout>
            <FormField label="Description">
              <Input value={description} onChange={({ detail }) => setDescription(detail.value)} />
            </FormField>
          </SpaceBetween>

          <SpaceBetween size="m">
            <Box variant="awsui-key-label">Address</Box>
            <FormField label="Address line 1" errorText={errors.address_line1}>
              <Input value={addressLine1} onChange={({ detail }) => setAddressLine1(detail.value)} />
            </FormField>
            <FormField label="Address line 2 (optional)">
              <Input value={addressLine2} onChange={({ detail }) => setAddressLine2(detail.value)} />
            </FormField>
            <ColumnLayout columns={3}>
              <FormField label="City" errorText={errors.city}>
                <Input value={city} onChange={({ detail }) => setCity(detail.value)} />
              </FormField>
              <FormField label="State / Province" errorText={errors.state_province}>
                <Input value={stateProvince} onChange={({ detail }) => setStateProvince(detail.value)} />
              </FormField>
              <FormField label="Postal code" errorText={errors.postal_code}>
                <Input value={postalCode} onChange={({ detail }) => setPostalCode(detail.value)} />
              </FormField>
            </ColumnLayout>
            <ColumnLayout columns={3}>
              <FormField label="Country" description="2-letter ISO (US, CA, GB, …)." errorText={errors.country}>
                <Input value={country} onChange={({ detail }) => setCountry(detail.value.toUpperCase())} />
              </FormField>
              <FormField label="Phone (optional)">
                <Input value={phone} onChange={({ detail }) => setPhone(detail.value)} />
              </FormField>
              <FormField label="Email (optional)" errorText={errors.email}>
                <Input type="email" value={email} onChange={({ detail }) => setEmail(detail.value)} />
              </FormField>
            </ColumnLayout>
          </SpaceBetween>

          <SpaceBetween size="m">
            <Box variant="awsui-key-label">Admin POC (required by ARIN)</Box>
            <PocFields
              namePrefix="admin"
              name={adminName} email={adminEmail} phone={adminPhone}
              setName={setAdminName} setEmail={setAdminEmail} setPhone={setAdminPhone}
              errors={errors}
            />
          </SpaceBetween>

          <SpaceBetween size="m">
            <Box variant="awsui-key-label">Tech POC (required by ARIN)</Box>
            <Checkbox
              checked={techSameAsAdmin}
              onChange={({ detail }) => setTechSameAsAdmin(detail.checked)}
            >
              Same as Admin POC
            </Checkbox>
            <PocFields
              namePrefix="tech"
              name={effTechName} email={effTechEmail} phone={effTechPhone}
              setName={setTechName} setEmail={setTechEmail} setPhone={setTechPhone}
              disabled={techSameAsAdmin}
              errors={errors}
            />
          </SpaceBetween>

          <SpaceBetween size="m">
            <Box variant="awsui-key-label">Abuse POC (required by ARIN)</Box>
            <Checkbox
              checked={abuseSameAsAdmin}
              onChange={({ detail }) => setAbuseSameAsAdmin(detail.checked)}
            >
              Same as Admin POC
            </Checkbox>
            <PocFields
              namePrefix="abuse"
              name={effAbuseName} email={effAbuseEmail} phone={effAbusePhone}
              setName={setAbuseName} setEmail={setAbuseEmail} setPhone={setAbusePhone}
              disabled={abuseSameAsAdmin}
              errors={errors}
            />
          </SpaceBetween>

          <SpaceBetween size="m">
            <Box variant="awsui-key-label">NOC POC (optional)</Box>
            <Checkbox
              checked={nocSameAsAdmin}
              onChange={({ detail }) => setNocSameAsAdmin(detail.checked)}
            >
              Same as Admin POC
            </Checkbox>
            <PocFields
              namePrefix="noc"
              name={effNocName} email={effNocEmail} phone={effNocPhone}
              setName={setNocName} setEmail={setNocEmail} setPhone={setNocPhone}
              disabled={nocSameAsAdmin}
              errors={errors}
            />
          </SpaceBetween>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function PocFields({
  namePrefix, name, email, phone, setName, setEmail, setPhone, errors, disabled,
}: Readonly<{
  namePrefix: string;
  name: string;
  email: string;
  phone: string;
  setName: (v: string) => void;
  setEmail: (v: string) => void;
  setPhone: (v: string) => void;
  errors: Record<string, string>;
  disabled?: boolean;
}>) {
  return (
    <ColumnLayout columns={3}>
      <FormField label="Name" errorText={errors[`${namePrefix}_poc_name`]}>
        <Input
          value={name}
          onChange={({ detail }) => setName(detail.value)}
          disabled={disabled}
        />
      </FormField>
      <FormField label="Email" errorText={errors[`${namePrefix}_poc_email`]}>
        <Input
          type="email"
          value={email}
          onChange={({ detail }) => setEmail(detail.value)}
          disabled={disabled}
        />
      </FormField>
      <FormField label="Phone">
        <Input
          value={phone}
          onChange={({ detail }) => setPhone(detail.value)}
          disabled={disabled}
        />
      </FormField>
    </ColumnLayout>
  );
}
