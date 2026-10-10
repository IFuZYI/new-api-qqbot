import type { ReactNode } from 'react'

/**
 * Shared UI kit. Colours come from the token layer in index.css (new-api's
 * palette), so every component follows the light/dark switch automatically and
 * no component hard-codes a colour.
 */

export function Card({ title, children, actions }: { title?: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="rounded-xl border border-border bg-card text-card-foreground shadow-sm">
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
          {title && <h2 className="text-sm font-medium">{title}</h2>}
          {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
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
    primary: 'border-transparent bg-primary text-primary-foreground hover:opacity-90',
    default: 'border-border bg-background text-foreground hover:bg-accent hover:text-accent-foreground',
    danger: 'border-transparent bg-destructive/10 text-destructive hover:bg-destructive/20',
    ghost: 'border-transparent bg-transparent text-muted-foreground hover:bg-accent hover:text-accent-foreground',
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

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-muted-foreground">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-muted-foreground">{hint}</span>}
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
      className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm text-foreground outline-none placeholder:text-muted-foreground focus:border-ring focus:ring-2 focus:ring-ring/30"
    />
  )
}

export function Select({
  value,
  onChange,
  children,
  className = '',
}: {
  value: string | number
  onChange: (v: string) => void
  children: ReactNode
  className?: string
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className={`rounded-md border border-input bg-background px-2 py-1.5 text-sm text-foreground outline-none focus:border-ring ${className}`}
    >
      {children}
    </select>
  )
}

export function Badge({
  children,
  tone = 'muted',
}: {
  children: ReactNode
  tone?: 'ok' | 'warn' | 'err' | 'muted' | 'accent' | 'info'
}) {
  const tones: Record<string, string> = {
    ok: 'text-success border-success/40 bg-success/10',
    warn: 'text-warning border-warning/40 bg-warning/10',
    err: 'text-destructive border-destructive/40 bg-destructive/10',
    accent: 'text-primary border-primary/40 bg-primary/10',
    info: 'text-info border-info/40 bg-info/10',
    muted: 'text-muted-foreground border-border bg-muted',
  }
  return (
    <span className={`inline-block rounded border px-1.5 py-0.5 text-[11px] leading-4 ${tones[tone]}`}>
      {children}
    </span>
  )
}

export function Table({ head, children }: { head: ReactNode[]; children: ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="text-left text-xs text-muted-foreground">
            {head.map((h, i) => (
              <th
                key={i}
                className="whitespace-nowrap border-b border-border bg-[var(--table-header)] px-3 py-2 font-normal"
              >
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
  return <p className="py-8 text-center text-xs text-muted-foreground">{children}</p>
}

export function ErrorNote({ children, onClose }: { children: ReactNode; onClose?: () => void }) {
  return (
    <div className="mb-3 flex items-start justify-between gap-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
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
    <div className="mb-3 rounded-md border border-success/40 bg-success/10 px-3 py-2 text-xs text-success">
      {children}
    </div>
  )
}

/** Modal shell shared by the dialogs. */
export function Modal({
  title,
  children,
  actions,
  onClose,
  wide,
}: {
  title: string
  children: ReactNode
  actions?: ReactNode
  onClose: () => void
  wide?: boolean
}) {
  return (
    <div
      className="fixed inset-0 z-10 flex items-start justify-center overflow-y-auto bg-black/50 p-4"
      onClick={onClose}
    >
      <div onClick={(e) => e.stopPropagation()} className={`my-8 w-full ${wide ? 'max-w-2xl' : 'max-w-md'}`}>
        <Card title={title} actions={actions}>
          {children}
        </Card>
      </div>
    </div>
  )
}
