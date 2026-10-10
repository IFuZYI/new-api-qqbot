import { useCallback, useEffect, useState } from 'react'
import { api, type Condition, type Rule, type RuleMeta } from './api'
import { Badge, Button, Card, Empty, Field, Input, Table } from './ui'

/** Group-conversion rules: when a member matches, move their new-api account. */
export default function Rules({
  onDone,
  onFail,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
}) {
  const [rules, setRules] = useState<Rule[]>([])
  const [meta, setMeta] = useState<RuleMeta | null>(null)
  const [groups, setGroups] = useState<string[]>([])
  const [editing, setEditing] = useState<Rule | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await api.rules()
      setRules(res.items ?? [])
    } catch (e) {
      onFail(e)
    }
  }, [onFail])

  useEffect(() => {
    void load()
    api.ruleMeta().then(setMeta).catch(onFail)
    // Group names come from the gateway; a failure here only costs the picker.
    api
      .newAPIGroups()
      .then((g) => setGroups(Object.keys(g).sort()))
      .catch(() => setGroups([]))
  }, [load, onFail])

  const blank = (): Rule => ({
    id: 0,
    name: '',
    enabled: true,
    on_bind: true,
    interval_minutes: 0,
    source_groups: [],
    target_group: '',
    conditions: [],
    last_run_at: 0,
  })

  const run = async (name: string, label: string) => {
    setBusy(true)
    try {
      const res = await api.action(name)
      onDone(`${label}：${JSON.stringify(res)}`)
      await load()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Card
        title="分组转换规则"
        actions={
          <>
            <Button disabled={busy} onClick={() => run('rules', '已执行巡检规则')}>
              立即执行巡检
            </Button>
            <Button kind="primary" onClick={() => setEditing(blank())}>
              新建规则
            </Button>
          </>
        }
      >
        {rules.length === 0 ? (
          <Empty>还没有规则。新建一条，选择触发条件与源/目标分组。</Empty>
        ) : (
          <Table head={['启用', '名称', '触发', '源分组', '目标分组', '条件', '上次执行', '操作']}>
            {rules.map((r) => (
              <tr key={r.id} className="border-b border-[var(--border)]/60">
                <td className="px-3 py-2">
                  {r.enabled ? <Badge tone="ok">启用</Badge> : <Badge>停用</Badge>}
                </td>
                <td className="px-3 py-2">{r.name}</td>
                <td className="px-3 py-2 text-xs">
                  {r.on_bind && <Badge tone="accent">绑定</Badge>}
                  {r.interval_minutes > 0 && (
                    <span className="ml-1">
                      <Badge>巡检 {r.interval_minutes}min</Badge>
                    </span>
                  )}
                </td>
                <td className="px-3 py-2 text-xs">
                  {r.source_groups.length ? r.source_groups.join(', ') : '任意'}
                </td>
                <td className="px-3 py-2 text-xs">{r.target_group}</td>
                <td className="px-3 py-2 text-xs">
                  {r.conditions.length === 0 ? (
                    <span className="text-[var(--muted)]">无</span>
                  ) : (
                    r.conditions
                      .map((c) => `${label(meta, c.type)} ${c.op} ${c.value}`)
                      .join(' 且 ')
                  )}
                </td>
                <td className="px-3 py-2 text-xs text-[var(--muted)]">
                  {r.last_run_at ? new Date(r.last_run_at * 1000).toLocaleString() : '—'}
                  {r.last_error && <div className="text-[var(--err)]">{r.last_error}</div>}
                </td>
                <td className="px-3 py-2">
                  <div className="flex gap-1">
                    <Button kind="ghost" onClick={() => setEditing(r)}>
                      编辑
                    </Button>
                    <Button
                      kind="ghost"
                      onClick={async () => {
                        try {
                          await api.deleteRule(r.id)
                          onDone(`已删除规则「${r.name}」`)
                          await load()
                        } catch (e) {
                          onFail(e)
                        }
                      }}
                    >
                      删除
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      {editing && (
        <RuleDialog
          rule={editing}
          meta={meta}
          groups={groups}
          onClose={() => setEditing(null)}
          onDone={async (msg) => {
            setEditing(null)
            onDone(msg)
            await load()
          }}
          onFail={onFail}
        />
      )}
    </>
  )
}

function label(meta: RuleMeta | null, value: string): string {
  return meta?.condition_types.find((c) => c.value === value)?.label ?? value
}

function RuleDialog({
  rule,
  meta,
  groups,
  onClose,
  onDone,
  onFail,
}: {
  rule: Rule
  meta: RuleMeta | null
  groups: string[]
  onClose: () => void
  onDone: (m: string) => void
  onFail: (e: unknown) => void
}) {
  const [r, setR] = useState<Rule>(rule)
  const [busy, setBusy] = useState(false)

  const set = (patch: Partial<Rule>) => setR((prev) => ({ ...prev, ...patch }))

  const toggleSource = (g: string) =>
    set({
      source_groups: r.source_groups.includes(g)
        ? r.source_groups.filter((x) => x !== g)
        : [...r.source_groups, g],
    })

  const addCondition = () =>
    set({
      conditions: [
        ...r.conditions,
        { type: meta?.condition_types[0]?.value ?? 'group_level', op: '>=', value: 0 } as Condition,
      ],
    })

  const setCondition = (i: number, patch: Partial<Condition>) =>
    set({ conditions: r.conditions.map((c, j) => (j === i ? { ...c, ...patch } : c)) })

  const removeCondition = (i: number) =>
    set({ conditions: r.conditions.filter((_, j) => j !== i) })

  const save = async () => {
    setBusy(true)
    try {
      const saved = r.id ? await api.updateRule(r) : await api.createRule(r)
      onDone(`已保存规则「${saved.name}」`)
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-10 flex items-start justify-center overflow-y-auto bg-black/60 p-4" onClick={onClose}>
      <div onClick={(e) => e.stopPropagation()} className="my-8 w-full max-w-2xl">
        <Card
          title={r.id ? `编辑规则：${rule.name}` : '新建规则'}
          actions={
            <>
              <Button onClick={onClose}>取消</Button>
              <Button kind="primary" disabled={busy} onClick={save}>
                保存
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="规则名称">
                <Input value={r.name} onChange={(v) => set({ name: v })} placeholder="例如 绑定即提升" />
              </Field>
              <Field label="启用">
                <select
                  value={r.enabled ? '1' : '0'}
                  onChange={(e) => set({ enabled: e.target.value === '1' })}
                  className="w-full rounded-md border border-[var(--border)] bg-[var(--panel)] px-3 py-1.5 text-sm"
                >
                  <option value="1">启用</option>
                  <option value="0">停用</option>
                </select>
              </Field>
            </div>

            <div>
              <div className="mb-2 text-xs text-[var(--muted)]">触发条件（至少选一个）</div>
              <div className="flex flex-wrap items-center gap-4">
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={r.on_bind}
                    onChange={(e) => set({ on_bind: e.target.checked })}
                  />
                  绑定成功时立即执行
                </label>
                <label className="flex items-center gap-2 text-sm">
                  巡检周期
                  <input
                    type="number"
                    min={0}
                    value={r.interval_minutes}
                    onChange={(e) => set({ interval_minutes: Number(e.target.value) })}
                    className="w-20 rounded-md border border-[var(--border)] bg-[var(--panel)] px-2 py-1 text-sm"
                  />
                  分钟（0 = 不巡检）
                </label>
              </div>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <div className="mb-1 text-xs text-[var(--muted)]">源分组（可多选，留空 = 任意）</div>
                <div className="max-h-40 space-y-1 overflow-y-auto rounded-md border border-[var(--border)] p-2">
                  {groups.length === 0 ? (
                    <p className="text-[11px] text-[var(--muted)]">
                      未读取到 new-api 分组（请在「连接配置」确认账号可登录）
                    </p>
                  ) : (
                    groups.map((g) => (
                      <label key={g} className="flex items-center gap-2 text-sm">
                        <input
                          type="checkbox"
                          checked={r.source_groups.includes(g)}
                          onChange={() => toggleSource(g)}
                        />
                        {g}
                      </label>
                    ))
                  )}
                </div>
              </div>
              <div>
                <div className="mb-1 text-xs text-[var(--muted)]">目标分组</div>
                <select
                  value={r.target_group}
                  onChange={(e) => set({ target_group: e.target.value })}
                  className="w-full rounded-md border border-[var(--border)] bg-[var(--panel)] px-3 py-1.5 text-sm"
                >
                  <option value="">请选择</option>
                  {groups.map((g) => (
                    <option key={g} value={g}>
                      {g}
                    </option>
                  ))}
                </select>
                <div className="mt-2">
                  <Field label="或手动填写">
                    <Input value={r.target_group} onChange={(v) => set({ target_group: v })} />
                  </Field>
                </div>
              </div>
            </div>

            <div>
              <div className="mb-2 flex items-center justify-between">
                <span className="text-xs text-[var(--muted)]">
                  附加条件（全部满足才触发；留空 = 只按触发条件）
                </span>
                <Button onClick={addCondition}>添加条件</Button>
              </div>
              {r.conditions.length === 0 ? (
                <p className="text-[11px] text-[var(--muted)]">无条件：所有匹配源分组的绑定成员都会被转换。</p>
              ) : (
                <div className="space-y-2">
                  {r.conditions.map((c, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <select
                        value={c.type}
                        onChange={(e) => setCondition(i, { type: e.target.value })}
                        className="rounded-md border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 text-sm"
                      >
                        {meta?.condition_types.map((t) => (
                          <option key={t.value} value={t.value}>
                            {t.label}
                          </option>
                        ))}
                      </select>
                      <select
                        value={c.op}
                        onChange={(e) => setCondition(i, { op: e.target.value })}
                        className="rounded-md border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 text-sm"
                      >
                        {meta?.operators.map((o) => (
                          <option key={o} value={o}>
                            {o}
                          </option>
                        ))}
                      </select>
                      <input
                        type="number"
                        value={c.value}
                        onChange={(e) => setCondition(i, { value: Number(e.target.value) })}
                        className="w-28 rounded-md border border-[var(--border)] bg-[var(--panel)] px-2 py-1.5 text-sm"
                      />
                      <Button kind="ghost" onClick={() => removeCondition(i)}>
                        移除
                      </Button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </Card>
      </div>
    </div>
  )
}
