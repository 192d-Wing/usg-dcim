// Schema-driven DoD NIC registration form. Given a template type + action, it
// renders only that template's visible sections/fields (honoring visibleWhen
// reveals such as Cloud Service Offering → CCS fields, and IPv4 vs IPv6
// registration-data blocks). Field definitions come from the shared schema in
// src/nic/templates.ts, so this never drifts from the backend validator.
import { useMemo } from 'react';

import Box from '@cloudscape-design/components/box';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import DatePicker from '@cloudscape-design/components/date-picker';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Textarea from '@cloudscape-design/components/textarea';
import Button from '@cloudscape-design/components/button';

import {
  Field,
  FormValues,
  fieldOptions,
  fieldRequired,
  fieldVisible,
  getTemplate,
  sectionVisible,
} from '@/nic/templates';

interface Props {
  templateType: string;
  action: string;
  values: FormValues;
  onChange: (key: string, value: unknown) => void;
  /** Read-only render (e.g. reviewing a submitted registration). */
  readOnly?: boolean;
}

export function NicRegistrationForm({ templateType, action, values, onChange, readOnly }: Props) {
  const tmpl = getTemplate(templateType);
  const visibleSections = useMemo(
    () => (tmpl ? tmpl.sections.filter((s) => sectionVisible(s, values)) : []),
    [tmpl, values],
  );
  if (!tmpl) return null;

  return (
    <SpaceBetween size="l">
      {visibleSections.map((sec) => (
        <Container key={sec.title} header={<Header variant="h3" description={sec.help}>{sec.title}</Header>}>
          <SpaceBetween size="m">
            {sec.fields
              .filter((f) => fieldVisible(f, values))
              .map((f) => (
                <FieldControl
                  key={f.key}
                  field={f}
                  action={action}
                  value={values[f.key]}
                  onChange={(v) => onChange(f.key, v)}
                  readOnly={readOnly}
                />
              ))}
          </SpaceBetween>
        </Container>
      ))}
    </SpaceBetween>
  );
}

function FieldControl({
  field: f,
  action,
  value,
  onChange,
  readOnly,
}: {
  field: Field;
  action: string;
  value: unknown;
  onChange: (v: unknown) => void;
  readOnly?: boolean;
}) {
  const required = fieldRequired(f, action);
  const label = (
    <>
      {f.label}
      {required ? ' *' : ''}
    </>
  );

  if (f.type === 'bool') {
    return (
      <Checkbox checked={Boolean(value)} disabled={readOnly} onChange={({ detail }) => onChange(detail.checked)}>
        {f.label}
        {required ? ' *' : ''}
      </Checkbox>
    );
  }

  return (
    <FormField label={label} description={f.help}>
      <FieldInput field={f} value={value} onChange={onChange} readOnly={readOnly} />
    </FormField>
  );
}

function FieldInput({
  field: f,
  value,
  onChange,
  readOnly,
}: {
  field: Field;
  value: unknown;
  onChange: (v: unknown) => void;
  readOnly?: boolean;
}) {
  if (f.repeat) {
    return <RepeatInput field={f} value={value} onChange={onChange} readOnly={readOnly} />;
  }
  switch (f.type) {
    case 'enum': {
      const opts = fieldOptions(f);
      const selected = opts.find((o) => o.value === String(value ?? '')) ?? null;
      return (
        <Select
          disabled={readOnly}
          selectedOption={selected ? { label: selected.label, value: selected.value } : null}
          options={opts.map((o) => ({ label: o.label, value: o.value }))}
          placeholder="Select…"
          onChange={({ detail }) => onChange(detail.selectedOption?.value ?? '')}
        />
      );
    }
    case 'text':
      return (
        <Textarea
          value={String(value ?? '')}
          disabled={readOnly}
          onChange={({ detail }) => onChange(detail.value)}
        />
      );
    case 'date':
      return (
        <DatePicker
          value={String(value ?? '')}
          disabled={readOnly}
          placeholder="YYYY/MM/DD"
          onChange={({ detail }) => onChange(detail.value)}
        />
      );
    case 'int':
      return (
        <Input
          type="number"
          value={value === undefined || value === null ? '' : String(value)}
          disabled={readOnly}
          onChange={({ detail }) => onChange(detail.value === '' ? undefined : Number(detail.value))}
        />
      );
    default:
      return (
        <Input
          type={f.type === 'email' ? 'email' : 'text'}
          value={String(value ?? '')}
          disabled={readOnly}
          onChange={({ detail }) => onChange(detail.value)}
        />
      );
  }
}

// RepeatInput renders a min..max list of scalar inputs (e.g. IP Address 1-6,
// DNS server hostnames) with add/remove affordances.
function RepeatInput({
  field: f,
  value,
  onChange,
  readOnly,
}: {
  field: Field;
  value: unknown;
  onChange: (v: unknown) => void;
  readOnly?: boolean;
}) {
  const items = Array.isArray(value) ? (value as unknown[]).map((v) => String(v ?? '')) : [''];
  const max = f.repeat?.max ?? 6;
  const setAt = (i: number, v: string) => {
    const next = [...items];
    next[i] = v;
    onChange(next);
  };
  const add = () => onChange([...items, '']);
  const removeAt = (i: number) => onChange(items.filter((_, idx) => idx !== i));

  return (
    <SpaceBetween size="xs">
      {items.map((it, i) => (
        <SpaceBetween key={i} size="xs" direction="horizontal">
          <Input
            value={it}
            disabled={readOnly}
            onChange={({ detail }) => setAt(i, detail.value)}
          />
          {!readOnly && items.length > 1 && (
            <Button iconName="remove" variant="icon" ariaLabel={`Remove ${f.label} ${i + 1}`} onClick={() => removeAt(i)} />
          )}
        </SpaceBetween>
      ))}
      {!readOnly && items.length < max && (
        <Box>
          <Button iconName="add-plus" onClick={add}>Add</Button>
        </Box>
      )}
    </SpaceBetween>
  );
}
