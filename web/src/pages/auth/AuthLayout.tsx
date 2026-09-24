import { useEffect, type ReactNode } from 'react';

const LOGO_URL = `${import.meta.env.BASE_URL}logo.svg`;

export interface AuthLayoutProps {
  /** Document + panel title. */
  title: string;
  subtitle?: ReactNode;
  children: ReactNode;
}

/** Centered panel with the logo, used by the login and first-run setup screens. */
export function AuthLayout({ title, subtitle, children }: AuthLayoutProps) {
  useEffect(() => {
    document.title = `${title} - Dupearr`;
  }, [title]);

  return (
    <div className="flex min-h-dvh items-start justify-center bg-page px-4 py-10 sm:items-center">
      <div className="w-full max-w-[400px]">
        <div className="flex flex-col items-center gap-3 rounded-t-lg bg-header px-6 pt-8 pb-6 text-header-fg">
          <img src={LOGO_URL} alt="" width={72} height={72} className="size-[72px]" />
          <div className="text-2xl font-semibold tracking-wide">Dupearr</div>
        </div>
        <div className="rounded-b-lg border border-t-0 border-border bg-card px-6 py-6 shadow-popover">
          <h1 className="m-0 text-lg font-normal text-fg-strong">{title}</h1>
          {subtitle && <div className="mt-1 text-sm text-muted">{subtitle}</div>}
          <div className="mt-5">{children}</div>
        </div>
      </div>
    </div>
  );
}

/** Stacked label + control used by the auth forms (FormGroup's side-by-side layout is too wide here). */
export function AuthField({
  label,
  htmlFor,
  errors,
  help,
  children,
}: {
  label: string;
  htmlFor: string;
  errors?: readonly string[];
  help?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className="text-sm font-semibold text-fg-strong">
        {label}
      </label>
      {children}
      {help && <div className="text-xs text-muted">{help}</div>}
      {errors?.map((e) => (
        <div key={e} role="alert" className="text-xs text-danger">
          {e}
        </div>
      ))}
    </div>
  );
}
