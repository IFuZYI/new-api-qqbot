import { useCallback, useEffect, useState } from 'react'
import { api, type AuthState, type Bootstrap, type Config, type Group, type Member, type MembersResponse, type Settings } from './api'
import Login from './Login'
import Rules from './Rules'
import { Badge, Button, Card, Empty, ErrorNote, Field, Input, OkNote, Table } from './ui'

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

  const checkAuth = useCallback(async () => {
    try {
      setAuth(await api.authState())
    } catch (e) {
      setErr(String(e))
    }
  }, [])

  const refresh = useCallback(async () => {
    try {
      setBoot(await api.bootstrap())
      setErr('')
    } catch (e) {
      setErr(String(e))
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
      <div className="flex min-h-full items-center justify-center p-4 text-xs text-[var(--muted)]">
        载入中…
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
      <header className="border-b border-[var(--border)] bg-[var(--panel)]">
        <div className="mx-auto flex max-w-6xl items-center justify-between px-4 py-3">
          <div className="flex items-baseline gap-3">
            <h1 className="text-base font-semibold">QQBot 群监测</h1>
            <span className="text-xs text-[var(--muted)]">QQ 群 ↔ new-api 绑定与签到</span>
          </div>
          <div className="flex items-center gap-3">
            {boot && (
              <Badge tone={boot.configured ? 'ok' : 'warn'}>
                {boot.configured ? '已配置' : '未配置'}
              </Badge>
            )}
            <span className="text-xs text-[var(--muted)]">{auth.username}</span>
            <Button kind="ghost" onClick={logout}>
              退出
            </Button>
          </div>
        </div>
        <nav className="mx-auto flex max-w-6xl gap-1 px-4">
          {TABS.map((t) => (
            <button
              key={t.id}
              onClick={() => setTab(t.id)}
              className={`-mb-px border-b-2 px-3 py-2 text-xs transition ${
                tab === t.id
                  ? 'border-[var(--accent)] text-[var(--fg)]'
                  : 'border-transparent text-[var(--muted)] hover:text-[var(--fg)]'
              }`}
            >
              {t.label}
            </button>
          ))}
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
        <p className="mt-3 text-xs text-[var(--muted)]">
          统计日期 {members?.date ?? '-'}　·　监测群 {boot?.groups?.filter((g) => g.enabled).length ?? 0} 个
        </p>
      </Card>

      <Card title="手动执行" actions={busy ? <Badge tone="accent">执行中…</Badge> : null}>
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
        <p className="mt-3 text-xs text-[var(--muted)]">
          这些操作也会按各自的周期自动执行；这里的按钮用于立即触发一次。
        </p>
      </Card>
    </>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md border border-[var(--border)] bg-[var(--panel)] px-3 py-2">
      <div className="text-xs text-[var(--muted)]">{label}</div>
      <div className="mt-1 text-xl font-semibold">{value}</div>
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
            <tr key={g.group_id} className="border-b border-[var(--border)]/60">
              <td className="px-3 py-2">
                <input
                  type="checkbox"
                  checked={selected.has(g.group_id)}
                  onChange={() => toggle(g.group_id)}
                />
              </td>
              <td className="px-3 py-2 font-mono text-xs">{g.group_id}</td>
              <td className="px-3 py-2">{g.group_name}</td>
              <td className="px-3 py-2 text-[var(--muted)]">{g.member_count}</td>
            </tr>
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
  const [data, setData] = useState<MembersResponse | null>(null)
  const [editing, setEditing] = useState<Member | null>(null)
  const [only, setOnly] = useState<'all' | 'bound' | 'unbound'>('all')

  const load = useCallback(async () => {
    try {
      setData(await api.members())
    } catch (e) {
      onFail(e)
    }
  }, [onFail])

  useEffect(() => {
    void load()
  }, [load])

  const items = (data?.items ?? []).filter((m) =>
    only === 'all' ? true : only === 'bound' ? m.bound : !m.bound,
  )

  return (
    <>
      <Card
        title="用户管理"
        actions={
          <>
            <select
              value={only}
              onChange={(e) => setOnly(e.target.value as typeof only)}
              className="rounded-md border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 text-xs"
            >
              <option value="all">全部</option>
              <option value="bound">已绑定</option>
              <option value="unbound">未绑定</option>
            </select>
            <Button onClick={load}>刷新</Button>
          </>
        }
      >
        {items.length === 0 ? (
          <Empty>没有成员。先到「群管理」刷新并同步成员。</Empty>
        ) : (
          <Table head={['QQ', '昵称', 'new-api 账号', '绑定方式', '今日已签到', '累计天数', '今日奖励', '操作']}>
            {items.map((m) => (
              <tr key={m.qq} className="border-b border-[var(--border)]/60">
                <td className="px-3 py-2 font-mono text-xs">{m.qq}</td>
                <td className="px-3 py-2">{m.nickname}</td>
                <td className="px-3 py-2">
                  {m.bound ? (
                    <span>
                      {m.newapi_username || '—'}
                      <span className="ml-1 text-xs text-[var(--muted)]">#{m.newapi_user_id}</span>
                    </span>
                  ) : (
                    <Badge tone="warn">未绑定</Badge>
                  )}
                </td>
                <td className="px-3 py-2 text-xs text-[var(--muted)]">{m.bind_source || '—'}</td>
                <td className="px-3 py-2">
                  {m.checked_today ? <Badge tone="ok">已签到</Badge> : <Badge>未签到</Badge>}
                </td>
                <td className="px-3 py-2">{m.checkin_days}</td>
                <td className="px-3 py-2">{m.reward_today || '—'}</td>
                <td className="px-3 py-2">
                  <div className="flex gap-1">
                    <Button kind="ghost" onClick={() => setEditing(m)}>
                      换绑
                    </Button>
                    {m.bound && (
                      <Button
                        kind="ghost"
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
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      {editing && (
        <RebindDialog
          member={editing}
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

function RebindDialog({
  member,
  onClose,
  onDone,
  onFail,
}: {
  member: Member
  onClose: () => void
  onDone: (m: string) => void
  onFail: (e: unknown) => void
}) {
  const [id, setId] = useState(String(member.newapi_user_id ?? ''))
  const [name, setName] = useState(member.newapi_username ?? '')

  const save = async () => {
    const n = Number(id)
    if (!Number.isInteger(n) || n <= 0) {
      onFail(new Error('new-api 账号 ID 必须是正整数'))
      return
    }
    try {
      await api.createBinding({ qq: member.qq, newapi_user_id: n, newapi_username: name })
      onDone(`已把 QQ ${member.qq} 换绑到 new-api 账号 #${n}`)
    } catch (e) {
      onFail(e)
    }
  }

  return (
    <div className="fixed inset-0 z-10 flex items-center justify-center bg-black/60 p-4" onClick={onClose}>
      <div onClick={(e) => e.stopPropagation()} className="w-full max-w-md">
        <Card title={`换绑 QQ ${member.qq}`}>
          <div className="space-y-3">
            <Field label="new-api 账号 ID" hint="在 new-api 后台用户列表里可以看到该账号的 ID">
              <Input value={id} onChange={setId} placeholder="例如 12" />
            </Field>
            <Field label="new-api 用户名（可选）" hint="仅用于显示">
              <Input value={name} onChange={setName} placeholder="例如 alice" />
            </Field>
            <div className="flex justify-end gap-2 pt-1">
              <Button onClick={onClose}>取消</Button>
              <Button kind="primary" onClick={save}>
                保存
              </Button>
            </div>
          </div>
        </Card>
      </div>
    </div>
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
        actions={
          <Button kind="primary" disabled={busy} onClick={save}>
            保存
          </Button>
        }
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="成员列表巡检周期（分钟）">
            <Input type="number" value={s.member_sync_minutes} onChange={num('member_sync_minutes')} />
          </Field>
          <Field label="打卡榜巡检周期（分钟）" hint="榜单只含当日且分页，周期短一些更不容易漏">
            <Input type="number" value={s.checkin_sync_minutes} onChange={num('checkin_sync_minutes')} />
          </Field>
          <Field label="签到奖励下限（quota）">
            <Input type="number" value={s.reward_min} onChange={num('reward_min')} />
          </Field>
          <Field label="签到奖励上限（quota）" hint="每天每人在区间内随机取一个值；上下限相同即固定">
            <Input type="number" value={s.reward_max} onChange={num('reward_max')} />
          </Field>
          <Field label="时区" hint="决定「今天」的边界，例如 Asia/Shanghai">
            <Input value={s.timezone} onChange={str('timezone')} />
          </Field>
        </div>
      </Card>

      <Card title={`修改密码（${username}）`}>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="当前密码">
            <Input type="password" value={cur} onChange={setCur} />
          </Field>
          <Field label="新密码" hint="至少 8 位">
            <Input type="password" value={next} onChange={setNext} />
          </Field>
          <div className="flex items-end">
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
          </div>
        </div>
      </Card>
    </>
  )
}

/* ---------------- connection ---------------- */

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
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api.getConfig().then(setC).catch(onFail)
  }, [onFail])

  if (!c) return <Card title="连接配置">载入中…</Card>

  const save = async () => {
    setBusy(true)
    try {
      setC(await api.saveConfig(c))
      onDone('连接配置已保存')
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card
      title="连接配置"
      actions={
        <Button kind="primary" disabled={busy} onClick={save}>
          保存
        </Button>
      }
    >
      <div className="space-y-5">
        <div>
          <h3 className="mb-3 text-xs font-medium text-[var(--muted)]">new-api 管理员账号</h3>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="URL">
              <Input value={c.newapi.url} onChange={(v) => setC({ ...c, newapi: { ...c.newapi, url: v } })} />
            </Field>
            <Field label="账号">
              <Input
                value={c.newapi.username}
                onChange={(v) => setC({ ...c, newapi: { ...c.newapi, username: v } })}
              />
            </Field>
            <Field label="密码" hint="留空或保持 ******** 表示不修改">
              <Input
                type="password"
                value={c.newapi.password}
                onChange={(v) => setC({ ...c, newapi: { ...c.newapi, password: v } })}
              />
            </Field>
          </div>
        </div>

        <div>
          <h3 className="mb-3 text-xs font-medium text-[var(--muted)]">NapCat</h3>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="WebUI URL" hint="用于读取/改写 OneBot 配置，并在没有 OneBot 适配器时兜底调用">
              <Input
                value={c.napcat.webui_url}
                onChange={(v) => setC({ ...c, napcat: { ...c.napcat, webui_url: v } })}
              />
            </Field>
            <Field label="WebUI Token" hint="留空或保持 ******** 表示不修改">
              <Input
                type="password"
                value={c.napcat.webui_token}
                onChange={(v) => setC({ ...c, napcat: { ...c.napcat, webui_token: v } })}
              />
            </Field>
            <Field label="OneBot HTTP URL" hint="巡检调用动作走这里，例如 http://127.0.0.1:3002">
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
              />
            </Field>
            <Field label="事件 WebSocket URL（可选）" hint="用于接收新成员进群与绑定指令；留空也能工作，只是反应慢一些">
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
              />
            </Field>
          </div>
        </div>
      </div>
    </Card>
  )
}
