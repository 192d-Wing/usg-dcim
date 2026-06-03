// Typed accessor over the canonical DoD NIC template schema. The JSON is
// synced from packages/otter-go/internal/nicreg/templates.json by
// scripts/sync-nic-schema.mjs (predev/prebuild) so the form here and the Go
// validator share one source of truth. Edit the canonical file, never the
// generated copy.
import raw from '@/nic/templates.gen.json';

export type EnumOption = { value: string; label: string };
export type Condition = { field: string; equals: string };
export type Repeat = { min: number; max: number };

export type FieldType =
  | 'string' | 'text' | 'email' | 'phone' | 'enum' | 'bool' | 'int' | 'date' | 'ip';

export interface Field {
  key: string;
  label: string;
  type: FieldType;
  required?: boolean;
  requiredForActions?: string[];
  help?: string;
  maxLength?: number;
  min?: number;
  max?: number;
  pattern?: string;
  options?: EnumOption[];
  enumRef?: string;
  visibleWhen?: Condition;
  repeat?: Repeat;
}

export interface Section {
  title: string;
  help?: string;
  visibleWhen?: Condition;
  fields: Field[];
}

export interface TemplateSchema {
  nicId: string;
  label: string;
  table: string;
  actions: string[];
  arinEligible: boolean;
  sections: Section[];
}

interface Schema {
  version: number;
  enums: Record<string, EnumOption[]>;
  templates: Record<string, TemplateSchema>;
}

const schema = raw as unknown as Schema;

export type FormValues = Record<string, unknown>;

/** The 8 template types as selector options, in schema order. */
export function templateOptions(): { value: string; label: string }[] {
  return Object.entries(schema.templates).map(([value, t]) => ({ value, label: t.label }));
}

export function getTemplate(type: string): TemplateSchema | undefined {
  return schema.templates[type];
}

export const ACTION_OPTIONS: EnumOption[] = schema.enums.action ?? [];

/** Resolve a field's allowed enum values (inline options or enumRef). */
export function fieldOptions(f: Field): EnumOption[] {
  if (f.options && f.options.length) return f.options;
  if (f.enumRef) return schema.enums[f.enumRef] ?? [];
  return [];
}

/** A nil condition is always met; otherwise compare the referenced value. */
export function conditionMet(cond: Condition | undefined, values: FormValues): boolean {
  if (!cond) return true;
  return String(values[cond.field] ?? '') === cond.equals;
}

export function sectionVisible(sec: Section, values: FormValues): boolean {
  return conditionMet(sec.visibleWhen, values);
}

export function fieldVisible(f: Field, values: FormValues): boolean {
  return conditionMet(f.visibleWhen, values);
}

export function fieldRequired(f: Field, action: string): boolean {
  if (f.required) return true;
  return (f.requiredForActions ?? []).includes(action);
}
