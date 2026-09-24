import { useId } from 'react';
import type { FieldSchema } from '@/api/types';
import { Checkbox, FormGroup, NumberInput, Select, TextArea, TextInput } from '@/components/ui';
import { humanize } from '@/lib/constants';
import type { FieldErrors } from './connectionUtils';
import { isMaskedSecret } from './connectionUtils';
import { isSecretField, toNumber, type SettingsValues } from './notificationForm';
import { SecretInput, SecretTextArea } from './SecretInput';

export interface SchemaFieldsProps {
  fields: readonly FieldSchema[] | null | undefined;
  values: SettingsValues;
  onChange: (name: string, value: unknown) => void;
  errors?: FieldErrors;
  disabled?: boolean;
}

/**
 * Renders provider fields from a notification schema: text / password / url / number / checkbox /
 * select / textarea, with required markers, help text and "advanced" fields hidden unless Show
 * Advanced is on. Secret fields use a masked password input; the "********" mask is kept as the
 * value until the user types a replacement (the server then keeps the stored secret).
 */
export function SchemaFields({ fields, values, onChange, errors = {}, disabled }: SchemaFieldsProps) {
  const prefix = useId();
  return (
    <>
      {(fields ?? []).map((field) => (
        <SchemaField
          key={field.name}
          id={`${prefix}-${field.name}`}
          field={field}
          value={values[field.name]}
          onChange={(v) => onChange(field.name, v)}
          errors={errors[field.name]}
          disabled={disabled}
        />
      ))}
    </>
  );
}

interface SchemaFieldProps {
  id: string;
  field: FieldSchema;
  value: unknown;
  onChange: (value: unknown) => void;
  errors?: string[];
  disabled?: boolean;
}

function asString(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return '';
  }
}

function SchemaField({ id, field, value, onChange, errors, disabled }: SchemaFieldProps) {
  const label = field.label || humanize(field.name);
  const invalid = !!errors && errors.length > 0;
  const secret = isSecretField(field);
  const masked = secret && isMaskedSecret(value);

  let control;
  if (secret && field.type === 'textarea') {
    control = (
      <SecretTextArea
        id={id}
        value={asString(value)}
        onChange={(v) => onChange(v)}
        invalid={invalid}
        required={field.required}
        disabled={disabled}
        data-secret="true"
      />
    );
  } else if (secret && field.type !== 'checkbox' && field.type !== 'number' && field.type !== 'select') {
    control = (
      <SecretInput
        id={id}
        value={asString(value)}
        onChange={(v) => onChange(v)}
        invalid={invalid}
        required={field.required}
        disabled={disabled}
        data-secret="true"
      />
    );
  } else {
    switch (field.type) {
      case 'checkbox':
        control = (
          <Checkbox id={id} checked={value === true || value === 'true'} onChange={(c) => onChange(c)} disabled={disabled} />
        );
        break;
      case 'number':
        control = (
          <NumberInput
            id={id}
            value={toNumber(value)}
            onChange={(n) => onChange(n)}
            allowEmpty={!field.required}
            integer={false}
            invalid={invalid}
            disabled={disabled}
          />
        );
        break;
      case 'select': {
        const options = (field.options ?? []).map((o) => ({ value: o, label: o }));
        const current = asString(value);
        if (current && !options.some((o) => o.value === current)) options.push({ value: current, label: current });
        control = (
          <Select
            id={id}
            options={options}
            value={current}
            onChange={(v) => onChange(v)}
            placeholder={field.required ? 'Select…' : '(none)'}
            invalid={invalid}
            disabled={disabled}
          />
        );
        break;
      }
      case 'textarea':
        control = (
          <TextArea
            id={id}
            value={asString(value)}
            onChange={(e) => onChange(e.target.value)}
            invalid={invalid}
            required={field.required}
            disabled={disabled}
            spellCheck={false}
          />
        );
        break;
      case 'url':
        control = (
          <TextInput
            id={id}
            value={asString(value)}
            onChange={(e) => onChange(e.target.value)}
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            invalid={invalid}
            required={field.required}
            disabled={disabled}
          />
        );
        break;
      default:
        control = (
          <TextInput
            id={id}
            value={asString(value)}
            onChange={(e) => onChange(e.target.value)}
            invalid={invalid}
            required={field.required}
            disabled={disabled}
          />
        );
    }
  }

  const help = masked ? (
    <>
      {field.helpText && <>{field.helpText} </>}
      <span className="text-subtle">Stored value hidden — type to replace it.</span>
    </>
  ) : (
    field.helpText
  );

  return (
    <FormGroup
      label={
        <>
          {label}
          {field.required && (
            <span className="ml-0.5 text-danger" aria-hidden>
              *
            </span>
          )}
        </>
      }
      htmlFor={id}
      helpText={help}
      errors={errors}
      // An advanced field with an error is shown even when "Show Advanced" is off.
      advanced={!!field.advanced && !invalid}
    >
      {control}
    </FormGroup>
  );
}
