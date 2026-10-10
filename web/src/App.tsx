import { useCallback, useEffect, useState } from 'react'
import { api, type AuthState, type Bootstrap, type CheckResult, type Config, type ConfigRevisions, type Group, type MembersResponse, type Settings, type UserFilter, type UserRow, type UsersResponse } from './api'
import Login from './Login'
import Rules from './Rules'
import { ThemeToggle } from './theme'
import { Badge, Button, Card, Cell, Combobox, Empty, ErrorNote, Field, FieldAction, Fieldset, Input, Modal, OkNote, Row, Select, Switch, Table, WarnNote } from './ui'

type Tab = 'overview' | 'groups' | 'users' | 'rules' | 'settings' | 'connection'

const TABS: { id: Tab; label: string }[] = [
  { id: 'overview', label: '概览' },
  { id: 'groups', label: '群管理' },
  { id: 'users', label: '用户管理' },
  { id: 'rules', label: '分组转换' },
  { id: 'settings', label: '监测设置' },
  { id: 'connection', label: '连接配置' },
]

export default function App() {
  const [auth, setAuth] = useState<AuthState | null>(null)
  const [tab, setTab] = useState<Tab>('overview')
  const [boot, setBoot] = useState<Bootstrap | null>(null)
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')

  const [authError, setAuthError] = useState('')

  const checkAuth = useCallback(async () => {
    try {
      setAuth(await api.authState())
      setAuthError('')
    } catch (e) {
      // Do NOT leave auth null with only the banner set: the banner lives
      // inside the authenticated branch, so the app would sit on the loading
      // screen forever with the error unreachable.
      setAuthError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const refresh = useCallback(async () => {
    try {
      setBoot(await api.bootstrap())
      setErr('')
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      // A 401 means the session expired or was revoked: drop back to the login
      // screen rather than leaving a dead panel showing zeros.
      if (msg.includes('未登录') || msg.includes('401')) {
        setAuth({ needs_setup: false, authenticated: false, username: '' })
        return
      }
      setErr(msg)
    }
  }, [])

  useEffect(() => {
    void checkAuth()
  }, [checkAuth])

  useEffect(() => {
    if (auth?.authenticated) void refresh()
  }, [auth, refresh])

  const flash = (msg: string) => {
    setOk(msg)
    setErr('')
    window.setTimeout(() => setOk(''), 4000)
  }
  const fail = (e: unknown) => {
    setErr(e instanceof Error ? e.message : String(e))
    setOk('')
  }

  if (!auth) {
    return (
      <div className="flex min-h-full items-center justify-center p-4">
        <div className="w-full max-w-sm text-center">
          {authError ? (
            <>
              <ErrorNote>{authError}</ErrorNote>
              <Button kind="primary" onClick={() => void checkAuth()}>
                重试
              </Button>
            </>
          ) : (
            <span className="text-xs text-muted-foreground">载入中…</span>
          )}
        </div>
      </div>
    )
  }

  if (!auth.authenticated) {
    return <Login needsSetup={auth.needs_setup} onDone={checkAuth} />
  }

  const logout = async () => {
    try {
      await api.logout()
      await checkAuth()
    } catch (e) {
      fail(e)
    }
  }

  return (
    <div className="min-h-full">
      <header className="sticky top-0 z-20 border-b border-border bg-background/85 backdrop-blur-sm">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-3 px-4 py-3">
          <div className="flex min-w-0 items-center gap-2.5">
            <span
              aria-hidden="true"
              className="flex size-7 shrink-0 items-center justify-center rounded-control bg-primary text-primary-foreground"
            >
              <svg viewBox="0 0 16 16" className="size-3.5">
                <path
                  d="M2.5 4.5h11M2.5 8h7.5M2.5 11.5h9.5"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.75"
                  strokeLinecap="round"
                />
              </svg>
            </span>
            <div className="min-w-0">
              <h1 className="truncate text-sm font-semibold tracking-tight">QQBot 群监测</h1>
              <p className="hidden truncate text-[11px] text-muted-foreground sm:block">
                QQ 群与 new-api 账号绑定
              </p>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {boot && (
              <Badge tone={boot.configured ? 'ok' : 'warn'}>
                {boot.configured ? '已配置' : '未配置'}
              </Badge>
            )}
            <span className="hidden text-xs text-muted-foreground sm:inline">{auth.username}</span>
            <ThemeToggle />
            <Button kind="ghost" onClick={logout} title="退出登录">
              退出
            </Button>
          </div>
        </div>
        {/*
          The strip scrolls rather than wraps: six tabs wrapped to 81px each on a
          390px screen against 38px on desktop, which doubled the header for no
          extra information.
        */}
        <nav className="mx-auto max-w-6xl px-4">
          <div className="tab-strip -mb-px flex gap-1 overflow-x-auto">
            {TABS.map((t) => (
              <button
                key={t.id}
                type="button"
                aria-current={tab === t.id ? 'page' : undefined}
                onClick={() => setTab(t.id)}
                className={`shrink-0 border-b-2 px-3 py-2 text-xs whitespace-nowrap transition-colors ${
                  tab === t.id
                    ? 'border-primary font-medium text-foreground'
                    : 'border-transparent text-muted-foreground hover:border-border hover:text-foreground'
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
        </nav>
      </header>

      <main className="mx-auto max-w-6xl space-y-4 p-4">
        {err && <ErrorNote onClose={() => setErr('')}>{err}</ErrorNote>}
        {ok && <OkNote>{ok}</OkNote>}

        {tab === 'overview' && <Overview boot={boot} onDone={flash} onFail={fail} onRefresh={refresh} />}
        {tab === 'groups' && <Groups onDone={flash} onFail={fail} onRefresh={refresh} />}
        {tab === 'users' && <Users onDone={flash} onFail={fail} onRefresh={refresh} />}
        {tab === 'rules' && <Rules onDone={flash} onFail={fail} />}
        {tab === 'settings' && (
          <SettingsView
            onDone={flash}
            onFail={fail}
            onRefresh={refresh}
            username={auth.username}
          />
        )}
        {tab === 'connection' && <Connection onDone={flash} onFail={fail} onRefresh={refresh} />}
      </main>
    </div>
  )
}

/* ---------------- overview ---------------- */

function Overview({
  boot,
  onDone,
  onFail,
  onRefresh,
}: {
  boot: Bootstrap | null
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [busy, setBusy] = useState('')
  const [members, setMembers] = useState<MembersResponse | null>(null)

  const load = useCallback(async () => {
    try {
      setMembers(await api.members())
    } catch (e) {
      onFail(e)
    }
  }, [onFail])

  useEffect(() => {
    void load()
  }, [load])

  const run = async (name: string, label: string) => {
    setBusy(name)
    try {
      const res = await api.action(name)
      onDone(`${label}完成：${JSON.stringify(res)}`)
      await load()
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy('')
    }
  }

  const total = members?.total ?? 0
  const bound = members?.items.filter((m) => m.bound).length ?? 0
  const checked = members?.items.filter((m) => m.checked_today).length ?? 0
  const awarded = members?.items.filter((m) => m.reward_today > 0).length ?? 0

  return (
    <>
      <Card title="状态">
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Stat label="群成员" value={total} />
          <Stat label="已绑定" value={bound} />
          <Stat label="今日已签到" value={checked} />
          <Stat label="今日已发放" value={awarded} />
        </div>
        <p className="mt-4 flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
          <span>统计日期 {members?.date ?? '-'}</span>
          <span aria-hidden="true">·</span>
          <span>监测群 {boot?.groups?.filter((g) => g.enabled).length ?? 0} 个</span>
        </p>
      </Card>

      <Card
        title="手动执行"
        description="这些操作也会按各自的周期自动执行；这里的按钮用于立即触发一次。"
        actions={busy ? <Badge tone="accent">执行中…</Badge> : null}
      >
        <div className="flex flex-wrap gap-2">
          <Button kind="primary" disabled={!!busy} onClick={() => run('sync', '同步成员')}>
            同步成员
          </Button>
          <Button disabled={!!busy} onClick={() => run('bind', '自动绑定')}>
            自动绑定
          </Button>
          <Button disabled={!!busy} onClick={() => run('promote', '分组提升')}>
            分组提升
          </Button>
          <Button disabled={!!busy} onClick={() => run('checkin', '同步打卡')}>
            同步打卡
          </Button>
          <Button disabled={!!busy} onClick={() => run('award', '发放奖励')}>
            发放奖励
          </Button>
        </div>
      </Card>
    </>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-panel border border-border bg-background px-3 py-2.5">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1 text-xl leading-7 font-semibold tabular-nums">{value}</div>
    </div>
  )
}

/* ---------------- groups ---------------- */

function Groups({
  onDone,
  onFail,
  onRefresh,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [groups, setGroups] = useState<Group[]>([])
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await api.groups()
      setGroups(res.items ?? [])
      setSelected(new Set((res.items ?? []).filter((g) => g.enabled).map((g) => g.group_id)))
    } catch (e) {
      onFail(e)
    }
  }, [onFail])

  useEffect(() => {
    void load()
  }, [load])

  const refresh = async () => {
    setBusy(true)
    try {
      const gs = await api.refreshGroups()
      setGroups(gs ?? [])
      setSelected(new Set((gs ?? []).filter((g) => g.enabled).map((g) => g.group_id)))
      onDone('群列表已从 QQ 刷新')
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  const save = async () => {
    setBusy(true)
    try {
      const gs = await api.saveGroups([...selected])
      setGroups(gs ?? [])
      onDone('监测群已保存')
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  const toggle = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <Card
      title="监测群"
      description="用开关选择要监测的群。新发现的群默认不监测，需要手动打开。"
      actions={
        <>
          <Button disabled={busy} onClick={refresh}>
            从 QQ 刷新
          </Button>
          <Button kind="primary" disabled={busy} onClick={save}>
            保存
          </Button>
        </>
      }
    >
      {groups.length === 0 ? (
        <Empty>还没有群。点「从 QQ 刷新」读取机器人账号所在的群。</Empty>
      ) : (
        <Table head={['监测', '群号', '群名', '人数']}>
          {groups.map((g) => (
            <Row key={g.group_id}>
              <Cell>
                <Switch
                  checked={selected.has(g.group_id)}
                  onChange={() => toggle(g.group_id)}
                  label={selected.has(g.group_id) ? '已监测' : '未监测'}
                />
              </Cell>
              <Cell className="font-mono text-xs">{g.group_id}</Cell>
              <Cell>{g.group_name}</Cell>
              <Cell className="text-muted-foreground tabular-nums">{g.member_count}</Cell>
            </Row>
          ))}
        </Table>
      )}
    </Card>
  )
}

/* ---------------- users ---------------- */

function Users({
  onDone,
  onFail,
  onRefresh,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [data, setData] = useState<UsersResponse | null>(null)
  const [editing, setEditing] = useState<UserRow | null>(null)
  const [groups, setGroups] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  // Filters. `q` is debounced into `keyword` so typing does not fire a request
  // per keystroke.
  const [bound, setBound] = useState<'' | 'bound' | 'unbound'>('')
  const [inGroup, setInGroup] = useState<'' | 'in' | 'out'>('')
  const [kind, setKind] = useState<'' | 'member' | 'left' | 'account'>('')
  const [group, setGroup] = useState('')
  const [q, setQ] = useState('')
  const [keyword, setKeyword] = useState('')

  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [batchGroup, setBatchGroup] = useState('')

  useEffect(() => {
    const t = window.setTimeout(() => setKeyword(q.trim()), 300)
    return () => window.clearTimeout(t)
  }, [q])

  const filter: UserFilter = { bound, in_group: inGroup, kind, group, q: keyword }

  const load = useCallback(async () => {
    try {
      setData(await api.users(filter))
    } catch (e) {
      onFail(e)
    }
    // filter is rebuilt each render; depend on its fields instead.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bound, inGroup, kind, group, keyword, onFail])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    // Group names come from the gateway; a failure only costs the picker.
    api
      .newAPIGroups()
      .then((g) => setGroups(Object.keys(g).sort()))
      .catch(() => setGroups([]))
  }, [])

  // Drop selections for rows that a filter change removed from the view, so a
  // batch never touches something the operator can no longer see.
  useEffect(() => {
    const visible = new Set((data?.items ?? []).map((r) => r.newapi_user_id).filter(Boolean))
    setSelected((prev) => {
      const next = new Set<number>()
      for (const id of prev) if (visible.has(id)) next.add(id)
      return next
    })
  }, [data])

  const items = data?.items ?? []
  const selectable = items.filter((r) => r.newapi_user_id)

  const toggle = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const toggleAll = () =>
    setSelected((prev) =>
      prev.size === selectable.length ? new Set() : new Set(selectable.map((r) => r.newapi_user_id!)),
    )

  const sync = async () => {
    setBusy(true)
    try {
      const res = await api.syncUsers()
      onDone(`已从 new-api 同步 ${res.accounts} 个账号`)
      await load()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  const applyBatch = async () => {
    if (selected.size === 0) return
    if (!batchGroup) {
      onFail(new Error('请先选择目标分组'))
      return
    }
    setBusy(true)
    try {
      const res = await api.setUserGroups([...selected], batchGroup)
      if (res.failed === 0) {
        onDone(`已把 ${res.succeeded} 个账号调整到「${batchGroup}」`)
      } else {
        const first = res.results.find((r) => !r.ok)
        onDone(`成功 ${res.succeeded} 个，失败 ${res.failed} 个${first ? `（例如 #${first.user_id}: ${first.error}）` : ''}`)
      }
      setSelected(new Set())
      await load()
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  const resetFilters = () => {
    setBound('')
    setInGroup('')
    setKind('')
    setGroup('')
    setQ('')
  }

  const s = data?.summary

  return (
    <>
      <Card
        title="用户管理"
        actions={
          <>
            <Button disabled={busy} onClick={sync}>
              从 new-api 同步账号
            </Button>
            <Button disabled={busy} onClick={load}>
              刷新
            </Button>
          </>
        }
      >
        {s && (
          <p className="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <span>共 {s.total} 行</span>
            <span aria-hidden="true">·</span>
            <span>在群 {s.in_group}</span>
            <span aria-hidden="true">·</span>
            <span>已退群 {s.left}</span>
            <span aria-hidden="true">·</span>
            <span>已绑定 {s.bound}</span>
            <span aria-hidden="true">·</span>
            <span>未绑定 {s.unbound}</span>
            <span aria-hidden="true">·</span>
            <span>未进群账号 {s.not_joined}</span>
          </p>
        )}

        <div className="mb-4 flex flex-wrap items-end gap-3">
          <div className="grid min-w-0 flex-1 basis-full gap-3 sm:grid-cols-2 lg:grid-cols-5">
            <Field label="绑定状态">
              <Select value={bound} onChange={(v) => setBound(v as typeof bound)} className="w-full">
                <option value="">全部</option>
                <option value="bound">已绑定</option>
                <option value="unbound">未绑定</option>
              </Select>
            </Field>
            <Field label="群状态">
              <Select value={inGroup} onChange={(v) => setInGroup(v as typeof inGroup)} className="w-full">
                <option value="">全部</option>
                <option value="in">在群</option>
                <option value="out">已退群 / 未进群</option>
              </Select>
            </Field>
            <Field label="分组">
              <Select value={group} onChange={setGroup} className="w-full">
                <option value="">全部分组</option>
                {groups.map((g) => (
                  <option key={g} value={g}>
                    {g}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="类型">
              <Select value={kind} onChange={(v) => setKind(v as typeof kind)} className="w-full">
                <option value="">全部</option>
                <option value="member">在群成员</option>
                <option value="left">已退群成员</option>
                <option value="account">未进群账号</option>
              </Select>
            </Field>
            <Field label="搜索" hint="QQ / 昵称 / new-api 用户名 / 邮箱">
              <Input value={q} onChange={setQ} placeholder="例如 54321 或 alice" />
            </Field>
          </div>
          {/* Clearing sits with the controls that set the filters, not below the table. */}
          <Button kind="ghost" onClick={resetFilters} disabled={busy}>
            清除筛选
          </Button>
        </div>

        {selected.size > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-panel border border-primary/40 bg-primary/5 px-3 py-2">
            <span className="text-xs font-medium">已选 {selected.size} 个账号</span>
            <Select value={batchGroup} onChange={setBatchGroup} ariaLabel="批量调整的目标分组">
              <option value="">选择目标分组…</option>
              {groups.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </Select>
            <Button kind="primary" disabled={busy || !batchGroup} onClick={applyBatch}>
              批量调整分组
            </Button>
            <Button kind="ghost" onClick={() => setSelected(new Set())}>
              取消选择
            </Button>
          </div>
        )}

        {items.length === 0 ? (
          <Empty>
            没有匹配的用户。先到「群管理」刷新并同步成员；要看到没人进群的 new-api 账号，请点「从
            new-api 同步账号」。
          </Empty>
        ) : (
          <Table
            stickyEnd={1}
            head={[
              <label
                key="all"
                className="flex size-6 cursor-pointer items-center justify-center"
                title="全选可调整的账号"
              >
                <input
                  type="checkbox"
                  className="size-4"
                  checked={selectable.length > 0 && selected.size === selectable.length}
                  onChange={toggleAll}
                  aria-label="全选可调整的账号"
                />
              </label>,
              'QQ',
              '昵称',
              'new-api 账号',
              '分组',
              '绑定方式',
              '今日已签到',
              '累计天数',
              '今日奖励',
              '操作',
            ]}
          >
            {items.map((m) => (
              <Row key={`${m.kind}-${m.qq}-${m.newapi_user_id ?? 0}`}>
                <Cell>
                  {m.newapi_user_id ? (
                    // The label carries the hit area: a bare 14px checkbox is well
                    // under the 24x24 minimum target size on a touch screen.
                    <label className="flex size-6 cursor-pointer items-center justify-center">
                      <input
                        type="checkbox"
                        className="size-4"
                        checked={selected.has(m.newapi_user_id)}
                        onChange={() => toggle(m.newapi_user_id!)}
                        aria-label={`选择账号 #${m.newapi_user_id}`}
                      />
                    </label>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </Cell>
                <Cell className="font-mono text-xs">{m.qq || '—'}</Cell>
                <Cell>
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    {m.nickname || '—'}
                    {m.kind === 'account' && <Badge tone="info">未进群</Badge>}
                    {m.kind === 'left' && <Badge tone="warn">已退群</Badge>}
                  </span>
                </Cell>
                <Cell>
                  {m.bound || m.kind === 'account' ? (
                    <span className="inline-flex items-center gap-1.5">
                      {m.newapi_username || '—'}
                      <span className="text-xs text-muted-foreground">#{m.newapi_user_id}</span>
                    </span>
                  ) : (
                    <Badge tone="warn">未绑定</Badge>
                  )}
                </Cell>
                <Cell>
                  {m.newapi_user_id ? (
                    <Select
                      value={m.newapi_group ?? ''}
                      ariaLabel={`账号 #${m.newapi_user_id} 的分组`}
                      onChange={async (v) => {
                        if (!v || v === m.newapi_group) return
                        try {
                          await api.setUserGroup(m.newapi_user_id!, v)
                          onDone(`已把 #${m.newapi_user_id} 调整到「${v}」`)
                          await load()
                          await onRefresh()
                        } catch (e) {
                          onFail(e)
                        }
                      }}
                    >
                      <option value={m.newapi_group ?? ''}>{m.newapi_group || '—'}</option>
                      {groups
                        .filter((g) => g !== m.newapi_group)
                        .map((g) => (
                          <option key={g} value={g}>
                            {g}
                          </option>
                        ))}
                    </Select>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </Cell>
                <Cell className="text-xs text-muted-foreground">{m.bind_source || '—'}</Cell>
                <Cell>
                  {m.checked_today ? <Badge tone="ok">已签到</Badge> : <Badge>未签到</Badge>}
                </Cell>
                <Cell className="tabular-nums">{m.checkin_days}</Cell>
                <Cell className="tabular-nums">{m.reward_today || '—'}</Cell>
                <Cell sticky>
                  <div className="flex justify-end gap-1.5">
                    <Button kind="ghost" size="sm" onClick={() => setEditing(m)}>
                      {m.bound ? '换绑' : m.kind === 'account' ? '绑定 QQ' : '绑定'}
                    </Button>
                    {m.bound && (
                      <Button
                        kind="ghost"
                        size="sm"
                        className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                        onClick={async () => {
                          try {
                            await api.deleteBinding(m.qq)
                            onDone(`已解绑 ${m.qq}`)
                            await load()
                            await onRefresh()
                          } catch (e) {
                            onFail(e)
                          }
                        }}
                      >
                        解绑
                      </Button>
                    )}
                  </div>
                </Cell>
              </Row>
            ))}
          </Table>
        )}
      </Card>

      {editing && (
        <RebindDialog
          member={editing}
          members={items}
          onClose={() => setEditing(null)}
          onDone={async (msg) => {
            setEditing(null)
            onDone(msg)
            await load()
            await onRefresh()
          }}
          onFail={onFail}
        />
      )}
    </>
  )
}

/**
 * RebindDialog links a QQ and a new-api account.
 *
 * It works from either side, because the operator arrives from whichever row
 * showed the problem: an unbound member (pick the account) or an account nobody
 * has joined with (pick the QQ). Both sides are dropdowns built from the rows
 * already on screen, so the operator selects real values instead of typing an
 * account id they would have to look up in new-api's own admin page.
 */
function RebindDialog({
  member,
  members,
  onClose,
  onDone,
  onFail,
}: {
  member: UserRow
  members: UserRow[]
  onClose: () => void
  onDone: (m: string) => void
  onFail: (e: unknown) => void
}) {
  // The row may be an account (no QQ yet) or a member (no account yet), so both
  // sides start from whatever it carries and are editable either way.
  const [qq, setQQ] = useState(member.qq ? String(member.qq) : '')
  const [accountID, setAccountID] = useState(member.newapi_user_id ? String(member.newapi_user_id) : '')

  // QQ candidates: every known member/left row, plus the row itself. keywords
  // carry the nickname so the operator can search by name instead of the number
  // the label leads with.
  const qqOptions = members
    .filter((m) => m.qq > 0)
    .map((m) => ({ value: String(m.qq), label: `${m.qq}${m.nickname ? `（${m.nickname}）` : ''}`, keywords: m.nickname }))
  // Account candidates: every known account, plus the row itself. The current
  // selection is kept even if the cache does not list it, so an existing
  // binding can always be re-saved unchanged.
  const accountOptions = members
    .filter((m) => m.newapi_user_id)
    .map((m) => ({
      value: String(m.newapi_user_id),
      label: `${m.newapi_username || '—'} #${m.newapi_user_id}${m.newapi_email ? `（${m.newapi_email}）` : ''}`,
      keywords: `${m.newapi_username || ''} ${m.newapi_user_id} ${m.newapi_email || ''}`,
    }))
  const seenQQ = new Set(qqOptions.map((o) => o.value))
  if (member.qq > 0 && !seenQQ.has(String(member.qq))) {
    qqOptions.unshift({ value: String(member.qq), label: `${member.qq}（${member.nickname || '—'}）`, keywords: member.nickname })
  }
  const seenAcc = new Set(accountOptions.map((o) => o.value))
  if (member.newapi_user_id && !seenAcc.has(String(member.newapi_user_id))) {
    accountOptions.unshift({
      value: String(member.newapi_user_id),
      label: `${member.newapi_username || '—'} #${member.newapi_user_id}`,
      keywords: `${member.newapi_username || ''} ${member.newapi_user_id} ${member.newapi_email || ''}`,
    })
  }

  const qqNum = Number(qq)
  const accNum = Number(accountID)
  const problem =
    !Number.isInteger(qqNum) || qqNum <= 0
      ? '请选择 QQ'
      : !Number.isInteger(accNum) || accNum <= 0
        ? '请选择 new-api 账号'
        : ''

  const save = async () => {
    if (problem) {
      onFail(new Error(problem))
      return
    }
    try {
      await api.createBinding({ qq: qqNum, newapi_user_id: accNum })
      onDone(`已把 QQ ${qqNum} 绑定到 new-api 账号 #${accNum}`)
    } catch (e) {
      onFail(e)
    }
  }

  return (
    <Modal
      title={member.bound ? `换绑 QQ ${member.qq || '—'}` : '绑定 QQ 与 new-api 账号'}
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose}>取消</Button>
          <Button kind="primary" disabled={problem !== ''} onClick={save}>
            保存
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="QQ 号" hint="从群成员里选择">
          <Combobox value={qq} onChange={setQQ} options={qqOptions} ariaLabel="选择 QQ 号" placeholder="搜索 QQ 号或昵称" emptyText="没有匹配的群成员" className="w-full" />
        </Field>
        <Field label="new-api 账号" hint="从已同步的账号里选择；账号邮箱是 qq@qq.com 时通常可自动绑定">
          <Combobox value={accountID} onChange={setAccountID} options={accountOptions} ariaLabel="选择 new-api 账号" placeholder="搜索用户名、账号 ID 或邮箱" emptyText="没有匹配的账号" className="w-full" />
        </Field>
        {accountOptions.length <= 1 && (
          <p className="text-[11px] leading-4 text-muted-foreground">
            账号列表为空？先在上方点「同步 new-api 账号」把账号拉取下来。
          </p>
        )}
        {problem && <WarnNote>{problem}</WarnNote>}
      </div>
    </Modal>
  )
}

/* ---------------- settings ---------------- */

function SettingsView({
  onDone,
  onFail,
  onRefresh,
  username,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
  username: string
}) {
  const [s, setS] = useState<Settings | null>(null)
  const [busy, setBusy] = useState(false)
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')

  useEffect(() => {
    api.settings().then(setS).catch(onFail)
  }, [onFail])

  if (!s) return <Card title="监测设置">载入中…</Card>

  const num = (k: keyof Settings) => (v: string) => setS({ ...s, [k]: Number(v) } as Settings)
  const str = (k: keyof Settings) => (v: string) => setS({ ...s, [k]: v } as Settings)

  const save = async () => {
    setBusy(true)
    try {
      setS(await api.saveSettings(s))
      onDone('设置已保存')
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Card
        title="监测设置"
        description="巡检周期与奖励规则；保存后立即生效。"
        actions={
          <Button kind="primary" disabled={busy} onClick={save}>
            {busy ? '保存中…' : '保存'}
          </Button>
        }
      >
        <div className="grid gap-x-6 gap-y-5 sm:grid-cols-2">
          <Field
            label="成员列表巡检"
            hint="兜底功能。新成员进群会立即记录，绑定成功也会立即记录；关闭后只在成员变动时同步昵称等资料。"
          >
            <div className="flex items-center justify-between gap-3">
              <Switch
                checked={s.member_patrol_enabled}
                onChange={(v) => setS({ ...s, member_patrol_enabled: v })}
                label={s.member_patrol_enabled ? '已启用' : '已停用'}
              />
              <div className="flex shrink-0 items-center gap-2">
                <Input
                  type="number"
                  min={0}
                  value={s.member_sync_minutes}
                  onChange={num('member_sync_minutes')}
                  ariaLabel="成员列表巡检间隔（分钟）"
                  className="w-20"
                />
                <span className="text-xs whitespace-nowrap text-muted-foreground">分钟</span>
              </div>
            </div>
          </Field>
          <Field
            label="打卡榜巡检"
            hint="打卡没有事件通知（NapCat 不提供），只能轮询榜单。关闭后签到不会被记录，也就不会发放奖励。"
          >
            <div className="flex items-center justify-between gap-3">
              <Switch
                checked={s.checkin_patrol_enabled}
                onChange={(v) => setS({ ...s, checkin_patrol_enabled: v })}
                label={s.checkin_patrol_enabled ? '已启用' : '已停用'}
              />
              <div className="flex shrink-0 items-center gap-2">
                <Input
                  type="number"
                  min={0}
                  value={s.checkin_sync_minutes}
                  onChange={num('checkin_sync_minutes')}
                  ariaLabel="打卡榜巡检间隔（分钟）"
                  className="w-20"
                />
                <span className="text-xs whitespace-nowrap text-muted-foreground">分钟</span>
              </div>
            </div>
          </Field>
          <Field label="签到奖励下限（quota）">
            <Input type="number" min={0} value={s.reward_min} onChange={num('reward_min')} className="w-40" />
          </Field>
          <Field label="签到奖励上限（quota）" hint="每天每人在区间内随机取一个值；上下限相同即固定">
            <Input type="number" min={0} value={s.reward_max} onChange={num('reward_max')} className="w-40" />
          </Field>
          <Field label="时区" hint="决定「今天」的边界，例如 Asia/Shanghai">
            <Input value={s.timezone} onChange={str('timezone')} className="w-56" />
          </Field>
        </div>
      </Card>

      <Card title="修改密码" description={`当前管理账号：${username}`}>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="当前密码">
            <Input type="password" value={cur} onChange={setCur} autoComplete="current-password" />
          </Field>
          <Field label="新密码" hint="至少 8 位">
            <Input type="password" value={next} onChange={setNext} autoComplete="new-password" />
          </Field>
          <FieldAction>
            <Button
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                try {
                  await api.changePassword(cur, next)
                  setCur('')
                  setNext('')
                  onDone('密码已修改')
                } catch (e) {
                  onFail(e)
                } finally {
                  setBusy(false)
                }
              }}
            >
              修改密码
            </Button>
          </FieldAction>
        </div>
      </Card>
    </>
  )
}

/* ---------------- connection ---------------- */

/**
 * CheckList renders the outcome of a connection test: one row per probed hop,
 * each with its own verdict, so a failure names the hop that is broken instead
 * of collapsing into a single "cannot connect".
 */
function CheckList({ results }: { results: CheckResult[] }) {
  return (
    <ul className="mt-3 space-y-1.5 border-t border-border pt-3">
      {results.map((r) => (
        <li key={r.name} className="flex items-start gap-2 text-xs">
          <span className="w-28 shrink-0 text-muted-foreground sm:w-32">{r.name}</span>
          {r.skipped ? (
            <Badge>跳过</Badge>
          ) : r.ok ? (
            <Badge tone="ok">成功</Badge>
          ) : (
            <Badge tone="err">失败</Badge>
          )}
          <span className={`min-w-0 break-all ${r.ok ? 'text-muted-foreground' : 'text-destructive'}`}>
            {r.error || r.detail}
          </span>
        </li>
      ))}
    </ul>
  )
}

function Connection({
  onDone,
  onFail,
  onRefresh,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [c, setC] = useState<Config | null>(null)
  const [revs, setRevs] = useState<ConfigRevisions>({ newapi: '', napcat: '' })
  const [busy, setBusy] = useState('')
  const [autoOpen, setAutoOpen] = useState(false)
  const [newapiCheck, setNewapiCheck] = useState<CheckResult[] | null>(null)
  const [napcatCheck, setNapcatCheck] = useState<CheckResult[] | null>(null)

  const apply = useCallback((r: { config: Config; revisions: ConfigRevisions }) => {
    setC(r.config)
    setRevs(r.revisions)
  }, [])

  const load = useCallback(() => {
    api
      .getConfig()
      .then((r) => apply(r))
      .catch(onFail)
  }, [apply, onFail])

  useEffect(() => {
    load()
  }, [load])

  if (!c) return <Card title="连接配置">载入中…</Card>

  // Testing is a separate action from saving: the operator can check what they
  // typed before committing it, and a failed test never blocks a save.
  const testNewAPI = async () => {
    setBusy('test-newapi')
    setNewapiCheck(null)
    try {
      const r = await api.testNewAPI({
        url: c.newapi.url,
        username: c.newapi.username,
        password: c.newapi.password,
      })
      setNewapiCheck(r.results)
    } catch (e) {
      onFail(e)
    } finally {
      setBusy('')
    }
  }

  const testNapCat = async () => {
    setBusy('test-napcat')
    setNapcatCheck(null)
    try {
      const r = await api.testNapCat({
        webui_url: c.napcat.webui_url,
        webui_token: c.napcat.webui_token,
        onebot_url: c.napcat.onebot_url,
        onebot_token: c.napcat.onebot_token,
        event_ws_url: c.napcat.event_ws_url,
        event_ws_token: c.napcat.event_ws_token,
      })
      setNapcatCheck(r.results)
    } catch (e) {
      onFail(e)
    } finally {
      setBusy('')
    }
  }

  // The two halves save independently, so a half-filled NapCat form can never
  // block (or overwrite) the gateway, and vice versa.
  const saveSection = async (section: 'newapi' | 'napcat', label: string) => {
    setBusy(section)
    try {
      const r =
        section === 'newapi'
          ? await api.saveNewAPIConfig(c, revs.newapi)
          : await api.saveNapCatConfig(c, revs.napcat)
      apply(r)
      onDone(`${label}已保存`)
      await onRefresh()
    } catch (e) {
      onFail(e)
      // The section changed underneath us (a hand edit or another tab): reload
      // so the operator sees the current values before deciding what to do.
      load()
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="space-y-4">
      <Card
        title="new-api 管理员账号"
        description="服务用这个账号登录 new-api 读取账号列表、调整分组与发放奖励。"
        actions={
          <>
            <Button disabled={busy !== ''} onClick={testNewAPI}>
              {busy === 'test-newapi' ? '测试中…' : '测试连接'}
            </Button>
            <Button kind="primary" disabled={busy !== ''} onClick={() => saveSection('newapi', 'new-api 连接配置')}>
              {busy === 'newapi' ? '保存中…' : '保存'}
            </Button>
          </>
        }
      >
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="URL" hint="例如 https://api.example.com">
            <Input
              value={c.newapi.url}
              onChange={(v) => setC({ ...c, newapi: { ...c.newapi, url: v } })}
              inputMode="text"
              autoComplete="url"
            />
          </Field>
          <Field label="账号">
            <Input
              value={c.newapi.username}
              onChange={(v) => setC({ ...c, newapi: { ...c.newapi, username: v } })}
              autoComplete="username"
            />
          </Field>
          <Field label="密码" hint="留空或保持 ******** 表示不修改">
            <Input
              type="password"
              value={c.newapi.password}
              onChange={(v) => setC({ ...c, newapi: { ...c.newapi, password: v } })}
              autoComplete="new-password"
            />
          </Field>
        </div>
        {newapiCheck && <CheckList results={newapiCheck} />}
      </Card>

      <Card
        title="NapCat"
        description="服务通过 NapCat 读取群成员与打卡榜、接收进群事件。"
        actions={
          <>
            <Button onClick={() => setAutoOpen(true)} disabled={busy !== ''}>
              自动配置
            </Button>
            <Button disabled={busy !== ''} onClick={testNapCat}>
              {busy === 'test-napcat' ? '测试中…' : '测试连接'}
            </Button>
            <Button kind="primary" disabled={busy !== ''} onClick={() => saveSection('napcat', 'NapCat 连接配置')}>
              {busy === 'napcat' ? '保存中…' : '保存'}
            </Button>
          </>
        }
      >
        <div className="mb-4 flex flex-wrap items-start gap-2 rounded-panel border border-info/40 bg-info/10 px-3 py-2.5 text-xs text-info">
          <span className="min-w-0 leading-5">
            只填 <b>WebUI 地址</b>与 <b>WebUI Token</b>，点「自动配置」即可：服务会登录 NapCat，
            自动开启 OneBot 的 HTTP 与 WebSocket 适配器，并把下面的 OneBot / 事件地址与 token 一并填好。
          </span>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="WebUI 地址" hint="浏览器里打开 NapCat 面板的地址，例如 http://127.0.0.1:6099">
            <Input
              value={c.napcat.webui_url}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, webui_url: v } })}
            />
          </Field>
          <Field label="WebUI Token" hint="NapCat 面板的访问 Token；留空或保持 ******** 表示不修改">
            <Input
              type="password"
              value={c.napcat.webui_token}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, webui_token: v } })}
              autoComplete="new-password"
            />
          </Field>
          <Field label="OneBot HTTP URL" hint="巡检调用动作走这里；自动配置会填好">
            <Input
              value={c.napcat.onebot_url}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, onebot_url: v } })}
            />
          </Field>
          <Field label="OneBot Token" hint="留空或保持 ******** 表示不修改">
            <Input
              type="password"
              value={c.napcat.onebot_token}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, onebot_token: v } })}
              autoComplete="new-password"
            />
          </Field>
          <Field label="事件 WebSocket URL（可选）" hint="接收新成员进群与绑定指令；留空也能工作，只是反应慢一些">
            <Input
              value={c.napcat.event_ws_url}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, event_ws_url: v } })}
            />
          </Field>
          <Field label="事件 WebSocket Token">
            <Input
              type="password"
              value={c.napcat.event_ws_token}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, event_ws_token: v } })}
              autoComplete="new-password"
            />
          </Field>
        </div>
        {napcatCheck && <CheckList results={napcatCheck} />}
      </Card>

      {autoOpen && (
        <AutoConfigureNapCat
          config={c}
          revision={revs.napcat}
          onClose={() => setAutoOpen(false)}
          onDone={(r) => {
            apply(r)
            setAutoOpen(false)
            onDone('NapCat 已自动配置：OneBot 适配器已开启，连接信息已保存')
            onRefresh()
          }}
          onFail={(e) => {
            onFail(e)
            load()
          }}
        />
      )}
    </div>
  )
}

/**
 * AutoConfigureNapCat asks only for the WebUI address (and token when it is not
 * already stored), then lets the server write NapCat's OneBot adapters. The
 * advanced ports are optional and default to the ones the deployment notes use.
 */
function AutoConfigureNapCat({
  config,
  revision,
  onClose,
  onDone,
  onFail,
}: {
  config: Config
  revision: string
  onClose: () => void
  onDone: (r: { config: Config; revisions: ConfigRevisions }) => void
  onFail: (e: unknown) => void
}) {
  const [url, setURL] = useState(config.napcat.webui_url)
  const [token, setToken] = useState('')
  const [httpPort, setHTTPPort] = useState('3002')
  const [wsPort, setWSPort] = useState('3001')
  // Empty means the server's default (127.0.0.1). A containerised deployment
  // must set 0.0.0.0 here, because the container can never reach the host's
  // loopback and an adapter bound to 127.0.0.1 would be unreachable.
  const [bindHost, setBindHost] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [busy, setBusy] = useState(false)

  const run = async () => {
    setBusy(true)
    try {
      const r = await api.autoConfigureNapCat({
        webui_url: url,
        webui_token: token,
        revision,
        http_port: Number(httpPort) || 0,
        ws_port: Number(wsPort) || 0,
        bind_host: bindHost.trim(),
      })
      onDone(r)
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="自动配置 NapCat"
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button kind="primary" onClick={run} disabled={busy || url.trim() === ''}>
            {busy ? '配置中…' : '开始配置'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <p className="text-xs leading-5 text-muted-foreground">
          服务会用它登录 NapCat，开启 OneBot 的 HTTP 与 WebSocket 适配器（无需重启），并把得到的地址与
          token 写回「连接配置」。
        </p>
        <Field label="WebUI 地址">
          <Input value={url} onChange={setURL} placeholder="http://127.0.0.1:6099" />
        </Field>
        <Field
          label="WebUI Token"
          hint={config.napcat.webui_token ? '留空则沿用已保存的 Token' : '首次配置必填'}
        >
          <Input type="password" value={token} onChange={setToken} autoComplete="new-password" />
        </Field>
        <div>
          <Button kind="ghost" size="sm" onClick={() => setShowAdvanced((v) => !v)}>
            {showAdvanced ? '收起高级选项' : '高级选项（适配器端口）'}
          </Button>
        </div>
        {showAdvanced && (
          <Fieldset legend="适配器端口" hint="不确定就留默认值；服务与 NapCat 同机时用默认端口即可。">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="OneBot HTTP 端口">
                <Input value={httpPort} onChange={setHTTPPort} inputMode="numeric" />
              </Field>
              <Field label="OneBot WebSocket 端口">
                <Input value={wsPort} onChange={setWSPort} inputMode="numeric" />
              </Field>
            </div>
            <Field
              label="适配器监听地址"
              hint="留空 = 127.0.0.1（本服务与 NapCat 同机）。Docker 部署必须填 0.0.0.0：容器访问不到宿主的 127.0.0.1。"
            >
              <Input value={bindHost} onChange={setBindHost} placeholder="0.0.0.0" />
            </Field>
          </Fieldset>
        )}
      </div>
    </Modal>
  )
}
