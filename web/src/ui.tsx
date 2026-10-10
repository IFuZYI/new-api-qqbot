import type { ReactNode } from 'react'

/** Small shared UI kit, styled with Tailwind and the CSS variables in index.css. */

export function Card({ title, children, actions }: { title?: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="rounded-lg border border-[var(--border)] bg-[var(--card)]">
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-[var(--border)] px-4 py-3">
          {title && <h2 className="text-sm font-medium">{title}</h2>}
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}

export function Button({
  children,
  onClick,
  kind = 'default',
  disabled,
  type = 'button',
}: {
  children: ReactNode
  onClick?: () => void
  kind?: 'default' | 'primary' | 'danger' | 'ghost'
  disabled?: boolean
  type?: 'button' | 'submit'
}) {
  const styles: Record<string, string> = {
    primary: 'bg-[var(--accent)] text-[var(--accent-fg)] hover:opacity-90 border-transparent',
    default: 'bg-[var(--panel)] text-[var(--fg)] hover:border-[var(--accent)] border-[var(--border)]',
    danger: 'bg-transparent text-[var(--err)] border-[var(--err)] hover:bg-[var(--err)]/10',
    ghost: 'bg-transparent text-[var(--muted)] border-transparent hover:text-[var(--fg)]',
  }
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`rounded-md border px-3 py-1.5 text-xs transition disabled:cursor-not-allowed disabled:opacity-50 ${styles[kind]}`}
    >
      {children}
    </button>
  )
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-[var(--muted)]">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-[var(--muted)]">{hint}</span>}
    </label>
  )
}

export function Input({
  value,
  onChange,
  type = 'text',
  placeholder,
}: {
  value: string | number
  onChange: (v: string) => void
  type?: string
  placeholder?: string
}) {
  return (
    <input
      type={type}
      value={value}
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
      className="w-full rounded-md border border-[var(--border)] bg-[var(--panel)] px-3 py-1.5 text-sm outline-none focus:border-[var(--accent)]"
    />
  )
}

export function Badge({ children, tone = 'muted' }: { children: ReactNode; tone?: 'ok' | 'warn' | 'err' | 'muted' | 'accent' }) {
  const tones: Record<string, string> = {
    ok: 'text-[var(--ok)] border-[var(--ok)]/40 bg-[var(--ok)]/10',
    warn: 'text-[var(--warn)] border-[var(--warn)]/40 bg-[var(--warn)]/10',
    err: 'text-[var(--err)] border-[var(--err)]/40 bg-[var(--err)]/10',
    accent: 'text-[var(--accent)] border-[var(--accent)]/40 bg-[var(--accent)]/10',
    muted: 'text-[var(--muted)] border-[var(--border)] bg-[var(--panel)]',
  }
  return (
    <span className={`inline-block rounded border px-1.5 py-0.5 text-[11px] leading-4 ${tones[tone]}`}>
      {children}
    </span>
  )
}

export function Table({ head, children }: { head: string[]; children: ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="text-left text-xs text-[var(--muted)]">
            {head.map((h) => (
              <th key={h} className="border-b border-[var(--border)] px-3 py-2 font-normal whitespace-nowrap">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="py-8 text-center text-xs text-[var(--muted)]">{children}</p>
}

export function ErrorNote({ children, onClose }: { children: ReactNode; onClose?: () => void }) {
  return (
    <div className="mb-3 flex items-start justify-between gap-3 rounded-md border border-[var(--err)]/40 bg-[var(--err)]/10 px-3 py-2 text-xs text-[var(--err)]">
      <span className="break-all">{children}</span>
      {onClose && (
        <button onClick={onClose} className="shrink-0 opacity-70 hover:opacity-100">
          ✕
        </button>
      )}
    </div>
  )
}

export function OkNote({ children }: { children: ReactNode }) {
  return (
    <div className="mb-3 rounded-md border border-[var(--ok)]/40 bg-[var(--ok)]/10 px-3 py-2 text-xs text-[var(--ok)]">
      {children}
    </div>
  )
}
