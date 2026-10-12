/**
 * LogsView is the 日志 tab: what the service has been doing, read straight from
 * the daily files.
 *
 * The page exists because a failure used to be visible only while someone was
 * watching the terminal, or as one line in a toast that scrolled away. Here the
 * operator picks a day, filters to a level, searches for a QQ number or an error
 * string, and reads the whole record with its attributes.
 *
 * Level filter and search are applied server-side (the file can be tens of
 * megabytes), so the counts shown next to the filter always describe the whole
 * day rather than the filtered result — "3 errors today" stays true while the
 * list is filtered to WARN.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type LogDay, type LogEntry, type LogResponse } from '../api'
import { Badge, Button, Card, Empty, ErrorNote, Field, Input, Select, Table, Cell } from '../ui'
import { formatDateTime } from '../format'

/** Levels the filter offers, strongest first. */
const LEVELS = [
  { value: '', label: '全部等级' },
  { value: 'debug', label: 'DEBUG 及以上' },
  { value: 'info', label: 'INFO 及以上' },
  { value: 'warn', label: 'WARN 及以上' },
  { value: 'error', label: '仅 ERROR' },
] as const

const LEVEL_TONE: Record<string, 'ok' | 'warn' | 'err' | 'info'> = {
  DEBUG: 'info',
  INFO: 'ok',
  WARN: 'warn',
  ERROR: 'err',
}

export default function LogsView({ onFail }: { onFail: (e: unknown) => void }) {
  const [res, setRes] = useState<LogResponse | null>(null)
  const [days, setDays] = useState<LogDay[]>([])
  const [dir, setDir] = useState('')
  // The day boundary is the operator's timezone, so the times are rendered in
  // it too: with the browser in another zone, a record filed under the
  // operator's day would otherwise read as a time on the day before.
  const [tz, setTz] = useState('')
  const [day, setDay] = useState('')
  const [level, setLevel] = useState('')
  const [search, setSearch] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  // daysRefreshedAt throttles the day-list request: it only changes when a day
  // rolls over or a file is written, so re-reading it on every keystroke of a
  // search is pure waste.
  const daysRefreshedAt = useRef(0)

  const load = useCallback(
    async (opts: { day: string; level: string; q: string }) => {
      setBusy(true)
      try {
        const r = await api.logs({
          day: opts.day || undefined,
          level: (opts.level || undefined) as never,
          q: opts.q || undefined,
        })
        setRes(r)
        setErr('')
        if (r.tz) setTz(r.tz)
        // Refresh the picker at most once a minute: it is a directory listing,
        // not part of the query.
        if (Date.now() - daysRefreshedAt.current > 60_000) {
          daysRefreshedAt.current = Date.now()
          const d = await api.logDays()
          setDays(d.days ?? [])
          setDir(d.dir)
          if (d.tz) setTz(d.tz)
        }
      } catch (e) {
        setErr(e instanceof Error ? e.message : String(e))
        onFail(e)
      } finally {
        setBusy(false)
      }
    },
    [onFail],
  )

  // The first load asks for today (no day parameter): the day picker's own
  // value is empty until the server says which day that is.
  useEffect(() => {
    daysRefreshedAt.current = 0
    void load({ day: '', level: '', q: '' })
  }, [load])

  const apply = (next: Partial<{ day: string; level: string; q: string }>) => {
    const q = { day, level, q: search, ...next }
    if (next.day !== undefined) setDay(next.day)
    if (next.level !== undefined) setLevel(next.level)
    if (next.q !== undefined) setSearch(next.q)
    void load(q)
  }

  const counts = res?.counts ?? {}
  const total = Object.values(counts).reduce((a, b) => a + b, 0)

  return (
    <div className="space-y-4">
      <Card
        title="日志"
        description="服务把每一条记录同时写到按天切分的文件和控制台。日志等级在「监测设置」里切换；这里只读。"
        actions={
          <Button size="sm" disabled={busy} onClick={() => apply({})}>
            {busy ? '刷新中…' : '刷新'}
          </Button>
        }
      >
        <div className="grid gap-x-6 gap-y-4 sm:grid-cols-3">
          <Field label="日期" hint={`文件目录：${dir || '—'}`}>
            <Select
              value={day}
              onChange={(v) => apply({ day: v })}
              ariaLabel="日志日期"
              className="w-44"
            >
              <option value="">今天（{res?.day ?? '…'}）</option>
              {days.map((d) => (
                <option key={d.day} value={d.day}>
                  {d.day}（{formatSize(d.size)}）
                </option>
              ))}
            </Select>
          </Field>
          <Field label="等级" hint="过滤只看，不影响记录什么">
            <Select value={level} onChange={(v) => apply({ level: v })} ariaLabel="日志等级" className="w-44">
              {LEVELS.map((l) => (
                <option key={l.value} value={l.value}>
                  {l.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="搜索" hint="匹配消息与属性值，例如 QQ 号、错误串">
            <form
              // Without preventDefault the browser performs a real navigation:
              // the SPA reloads, the filter is lost and the operator is back on
              // the 概览 tab. The form exists so Enter submits.
              onSubmit={(e) => {
                e.preventDefault()
                apply({})
              }}
            >
              <Input
                value={search}
                onChange={setSearch}
                placeholder="关键字，回车搜索"
                ariaLabel="日志搜索"
                className="w-full"
              />
            </form>
          </Field>
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted-foreground">
            {res?.day ?? '今天'}
            {tz ? `（${tz}）` : ''} 共 {total} 条：
          </span>
          {(['ERROR', 'WARN', 'INFO', 'DEBUG'] as const).map((lv) => (
            <Badge key={lv} tone={counts[lv] ? LEVEL_TONE[lv] : undefined}>
              {lv} {counts[lv] ?? 0}
            </Badge>
          ))}
          {res?.truncated && <span className="text-2xs text-muted-foreground">只显示最新的若干条，请缩小范围</span>}
        </div>

        {err && <div className="mt-3"><ErrorNote>{err}</ErrorNote></div>}
      </Card>

      <Card title="记录">
        {!res ? (
          '载入中…'
        ) : res.entries.length === 0 ? (
          <Empty>这一天没有符合条件的记录。</Empty>
        ) : (
          <Table head={['时间', '等级', '内容']}>
            {res.entries.map((e, i) => (
              <tr key={`${e.time}-${i}`} className="align-top">
                <Cell className="whitespace-nowrap text-muted-foreground tabular-nums">
                  {formatClock(e.time, tz)}
                </Cell>
                <Cell>
                  <Badge tone={LEVEL_TONE[e.level]}>{e.level}</Badge>
                </Cell>
                <Cell className="min-w-0">
                  <div className="break-all text-sm">{e.msg}</div>
                  <Attrs entry={e} />
                </Cell>
              </tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  )
}

/**
 * Attrs renders a record's extra fields. They are what makes a record usable:
 * the message says "巡检失败", the attributes say which patrol and why.
 */
function Attrs({ entry }: { entry: LogEntry }) {
  const attrs = entry.attrs
  if (!attrs || Object.keys(attrs).length === 0) return null
  return (
    <div className="mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-2xs text-muted-foreground">
      {Object.entries(attrs).map(([k, v]) => (
        <span key={k} className="break-all">
          <span className="font-medium">{k}</span>={clip(String(v))}
        </span>
      ))}
    </div>
  )
}

/**
 * clip bounds one attribute value. A gateway error is a paragraph, and printing
 * it in full pushed everything else off the row; the full text is still in the
 * file, and the search matches it.
 */
function clip(s: string, max = 240): string {
  return s.length > max ? `${s.slice(0, max)}…` : s
}

/**
 * formatClock renders a record's timestamp in the operator's timezone, which is
 * the calendar the selected day is in.
 */
function formatClock(iso: string, tz: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  if (!tz) return formatDateTime(Math.floor(t / 1000))
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: tz,
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
      hour12: false,
    }).format(new Date(t))
  } catch {
    // An unknown zone name must not blank the row: fall back to local time.
    return formatDateTime(Math.floor(t / 1000))
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
