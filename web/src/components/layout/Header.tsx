import { clsx } from 'clsx';
import { LogOut, Menu, Moon, Search, Sun, X } from 'lucide-react';
import { useState, type FormEvent, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { useServerEvents } from '@/api/events';
import { useLogout } from '@/api/hooks/useAuth';
import { useAppInfo } from '@/app/AppInfo';
import { useTheme } from '@/app/preferences';
import { Spinner } from '@/components/ui/Spinner';
import { HealthIndicator } from './HealthIndicator';

const LOGO_URL = `${import.meta.env.BASE_URL}logo.svg`;

function HeaderIconButton({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="inline-flex size-9 items-center justify-center rounded text-header-fg/80 transition-colors hover:bg-white/10 hover:text-header-fg"
    >
      {children}
    </button>
  );
}

/** Global search: submits to `/?search=<term>` (the Duplicates page reads it). */
function GlobalSearch({ className }: { className?: string }) {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const urlTerm = params.get('search') ?? '';
  const [term, setTerm] = useState(urlTerm);
  const [lastUrlTerm, setLastUrlTerm] = useState(urlTerm);
  // Follow external changes of ?search= (e.g. the Duplicates page clearing its filter).
  if (urlTerm !== lastUrlTerm) {
    setLastUrlTerm(urlTerm);
    setTerm(urlTerm);
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const q = term.trim();
    navigate(q ? `/?search=${encodeURIComponent(q)}` : '/');
  };

  return (
    <form role="search" onSubmit={submit} className={clsx('relative', className)}>
      <Search
        aria-hidden
        width={16}
        height={16}
        className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-header-fg/60"
      />
      <input
        type="search"
        value={term}
        onChange={(e) => setTerm(e.target.value)}
        placeholder="Search duplicates"
        aria-label="Search duplicates"
        className={clsx(
          'h-9 w-full rounded border border-transparent bg-white/8 pr-8 pl-8 text-sm text-header-fg placeholder:text-header-fg/50',
          'outline-none focus:border-accent focus:bg-white/12',
          '[&::-webkit-search-cancel-button]:appearance-none',
        )}
      />
      {term && (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => {
            setTerm('');
            if (params.get('search')) navigate('/');
          }}
          className="absolute top-1/2 right-1.5 -translate-y-1/2 rounded p-1 text-header-fg/60 hover:text-header-fg"
        >
          <X width={14} height={14} aria-hidden />
        </button>
      )}
    </form>
  );
}

/** Scan progress text shown in the header while a scan runs. */
function ScanProgressIndicator() {
  const { scanProgress } = useServerEvents();
  if (!scanProgress) return null;
  return (
    <div
      className="hidden max-w-[28vw] min-w-0 items-center gap-2 text-xs text-header-fg/80 lg:flex"
      title={scanProgress.message}
      aria-live="polite"
    >
      <Spinner size="sm" className="shrink-0 text-accent-soft" label="Scanning" />
      <span className="truncate">{scanProgress.message}</span>
    </div>
  );
}

export interface HeaderProps {
  onToggleSidebar: () => void;
  sidebarOpen: boolean;
}

/** Top bar: sidebar toggle, logo, global search, scan progress, health, theme, logout. */
export function Header({ onToggleSidebar, sidebarOpen }: HeaderProps) {
  const info = useAppInfo();
  const [theme, , toggleTheme] = useTheme();
  const logout = useLogout();
  const showLogout = info?.authenticationMethod === 'Forms';

  return (
    <header className="z-30 flex h-[60px] shrink-0 items-center gap-2 bg-header px-2 text-header-fg shadow-[0_1px_0_rgb(0_0_0/0.25)] sm:gap-3 sm:px-3">
      <HeaderIconButton label={sidebarOpen ? 'Hide sidebar' : 'Show sidebar'} onClick={onToggleSidebar}>
        <Menu width={20} height={20} aria-hidden />
      </HeaderIconButton>

      <Link to="/" className="flex shrink-0 items-center gap-2 text-header-fg no-underline" aria-label="Dupearr home">
        <img src={LOGO_URL} alt="" width={32} height={32} className="size-8" />
        <span className="hidden text-lg font-semibold tracking-wide sm:inline">Dupearr</span>
      </Link>

      <GlobalSearch className="mx-1 w-full max-w-[320px] min-w-0 flex-1 sm:mx-3" />

      <div className="ml-auto flex shrink-0 items-center gap-1 sm:gap-2">
        <ScanProgressIndicator />
        <HealthIndicator />
        <HeaderIconButton
          label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
          onClick={toggleTheme}
        >
          {theme === 'dark' ? <Sun width={19} height={19} aria-hidden /> : <Moon width={19} height={19} aria-hidden />}
        </HeaderIconButton>
        {showLogout && (
          <HeaderIconButton label="Log out" onClick={logout}>
            <LogOut width={19} height={19} aria-hidden />
          </HeaderIconButton>
        )}
      </div>
    </header>
  );
}
