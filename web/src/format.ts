/**
 * One date/time formatter for the whole panel.
 *
 * A bare `new Date(x).toLocaleString()` renders the *runtime's* default locale,
 * which measured as the US 12-hour form (`10/11/2026, 12:36:51 PM`) inside a
 * panel whose every other surface is Simplified Chinese. Every user-visible
 * timestamp goes through this one formatter so the two never diverge.
 */
const dateTime = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
})

/**
 * formatDateTime renders a unix-seconds timestamp as `2026/10/11 12:36:51`.
 * A missing or zero value reads as `—`; an unparseable value is returned as it
 * is rather than as "Invalid Date".
 */
export function formatDateTime(unixSeconds?: number): string {
  if (!unixSeconds) return '—'
  const d = new Date(unixSeconds * 1000)
  if (Number.isNaN(d.getTime())) return String(unixSeconds)
  return dateTime.format(d)
}
