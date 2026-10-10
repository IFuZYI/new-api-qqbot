import { useCallback, useEffect, useState } from 'react'
import { api, type Condition, type Rule, type RuleMeta } from './api'
import { Badge, Button, Card, Empty, Field, Input, Modal, MultiSelect, Select, Switch, Table } from './ui'

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
  const [groupsErr, setGroupsErr] = useState('')
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

  // Group names come from the gateway. A failure only costs the pickers, so it
  // is reported inline (with a retry) instead of as a global error banner.
  const loadGroups = useCallback(() => {
    setGroupsErr('')
    api
      .newAPIGroups()
      .then((g) => setGroups(Object.keys(g).sort()))
      .catch((e) => {
        setGroups([])
        setGroupsErr(e instanceof Error ? e.message : String(e))
      })
  }, [])

  useEffect(() => {
    void load()
    api.ruleMeta().then(setMeta).catch(onFail)
    loadGroups()
  }, [load, loadGroups, onFail])

  const run = async () => {
    setBusy(true)
    try {
      const res = await api.action('rules')
      onDone(`已执行巡检规则：${JSON.stringify(res)}`)
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
            <Button disabled={busy} onClick={run}>
              {busy ? '执行中…' : '立即执行巡检'}
            </Button>
            <Button kind="primary" onClick={() => setEditing(blankRule())}>
              新建规则
            </Button>
          </>
        }
      >
        <p className="mb-3 text-xs text-muted-foreground">
          规则在<b>绑定成功时</b>和/或<b>巡检周期</b>执行；源分组留空表示任意分组。既无触发方式又无条件的规则
          不会匹配任何人。
        </p>
        {groupsErr && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning">
            <span className="break-all">读取 new-api 分组失败：{groupsErr}（源/目标分组需手动填写）</span>
            <Button kind="ghost" onClick={loadGroups}>
              重试
            </Button>
          </div>
        )}
        {rules.length === 0 ? (
          <Empty>还没有规则。新建一条，选择触发条件与源/目标分组。</Empty>
        ) : (
          <Table head={['启用', '名称', '触发', '源分组', '目标分组', '条件', '上次执行', '操作']}>
            {rules.map((r) => (
              <tr key={r.id} className="border-b border-border/60">
                <td className="px-3 py-2">
                  <Badge tone={r.enabled ? 'ok' : 'muted'}>{r.enabled ? '启用' : '停用'}</Badge>
                </td>
                <td className="px-3 py-2">{r.name}</td>
                <td className="px-3 py-2 text-xs">
                  <div className="flex flex-wrap gap-1">
                    {r.on_bind && <Badge tone="accent">绑定</Badge>}
                    {r.interval_minutes > 0 && <Badge>巡检 {r.interval_minutes}min</Badge>}
                    {!r.on_bind && r.interval_minutes <= 0 && (
                      <span className="text-muted-foreground">无触发</span>
                    )}
                  </div>
                </td>
                <td className="px-3 py-2 text-xs">
                  {r.source_groups.length ? (
                    <div className="flex flex-wrap gap-1">
                      {r.source_groups.map((g) => (
                        <Badge key={g}>{g}</Badge>
                      ))}
                    </div>
                  ) : (
                    <span className="text-muted-foreground">任意</span>
                  )}
                </td>
                <td className="px-3 py-2 text-xs">
                  <Badge tone="info">{r.target_group}</Badge>
                </td>
                <td className="px-3 py-2 text-xs">
                  {r.conditions.length === 0 ? (
                    <span className="text-muted-foreground">无</span>
                  ) : (
                    <div className="space-y-0.5">
                      {r.conditions.map((c, i) => (
                        <div key={i}>
                          {label(meta, c.type)} {c.op} {c.value}
                        </div>
                      ))}
                    </div>
                  )}
                </td>
                <td className="px-3 py-2 text-xs text-muted-foreground">
                  {r.last_run_at ? new Date(r.last_run_at * 1000).toLocaleString() : '—'}
                  {r.last_error && <div className="text-destructive">{r.last_error}</div>}
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

/** blankRule is a new rule: enabled, triggered on bind, nothing selected. */
function blankRule(): Rule {
  return {
    id: 0,
    name: '',
    enabled: true,
    on_bind: true,
    interval_minutes: 0,
    source_groups: [],
    target_group: '',
    conditions: [],
    last_run_at: 0,
  }
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

  // The rule needs a name, a target and at least one trigger; catching it here
  // keeps the dialog open with the reason instead of round-tripping a 400.
  const problem = (): string => {
    if (!r.name.trim()) return '请填写规则名称'
    if (!r.target_group.trim()) return '请选择目标分组'
    if (!r.on_bind && r.interval_minutes <= 0) return '至少选择一种触发方式：绑定触发或巡检周期'
    if (r.source_groups.includes(r.target_group)) return '源分组与目标分组不能相同'
    return ''
  }
  const problemText = problem()

  const save = async () => {
    if (problemText) return
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

  // A target that is no longer on the gateway must stay selectable, or opening
  // and re-saving an existing rule would silently change it.
  const targetOptions = groups.includes(r.target_group) || !r.target_group
    ? groups
    : [...groups, r.target_group].sort()

  return (
    <Modal
      wide
      title={r.id ? `编辑规则：${rule.name}` : '新建规则'}
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose}>取消</Button>
          <Button kind="primary" disabled={busy || problemText !== ''} onClick={save}>
            保存
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="规则名称">
            <Input value={r.name} onChange={(v) => set({ name: v })} placeholder="例如 绑定即提升" />
          </Field>
          <Field label="启用" hint="停用的规则不参与绑定触发，也不参与巡检">
            <div className="pt-1">
              <Switch
                checked={r.enabled}
                onChange={(v) => set({ enabled: v })}
                label={r.enabled ? '已启用' : '已停用'}
              />
            </div>
          </Field>
        </div>

        <div className="rounded-md border border-border p-3">
          <div className="mb-3 text-xs font-medium text-muted-foreground">触发条件（至少选一个）</div>
          <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
            <Switch
              checked={r.on_bind}
              onChange={(v) => set({ on_bind: v })}
              label="绑定成功时立即执行"
            />
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">巡检周期</span>
              <input
                type="number"
                min={0}
                value={r.interval_minutes}
                onChange={(e) => set({ interval_minutes: Number(e.target.value) })}
                className="w-20 rounded-md border border-input bg-background px-2 py-1.5 text-sm outline-none focus:border-ring"
              />
              <span className="text-xs text-muted-foreground">分钟（0 = 不巡检）</span>
            </div>
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="源分组" hint="可多选；留空 = 任意分组">
            <MultiSelect
              options={groups}
              selected={r.source_groups}
              onChange={(v) => set({ source_groups: v })}
              allText="任意分组"
              emptyText="未读取到 new-api 分组（请在「连接配置」确认账号可登录）"
            />
          </Field>
          <Field label="目标分组" hint="账号被移动到这个分组">
            <Select
              value={r.target_group}
              onChange={(v) => set({ target_group: v })}
              className="w-full"
            >
              <option value="">请选择</option>
              {targetOptions.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </Select>
          </Field>
        </div>

        <div>
          <div className="mb-2 flex items-center justify-between">
            <span className="text-xs text-muted-foreground">
              附加条件（全部满足才触发；留空 = 只按触发条件）
            </span>
            <Button onClick={addCondition}>添加条件</Button>
          </div>
          {r.conditions.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">
              无条件：所有匹配源分组的绑定成员都会被转换。
            </p>
          ) : (
            <div className="space-y-2">
              {r.conditions.map((c, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2">
                  <Select value={c.type} onChange={(v) => setCondition(i, { type: v })}>
                    {meta?.condition_types.map((t) => (
                      <option key={t.value} value={t.value}>
                        {t.label}
                      </option>
                    ))}
                  </Select>
                  <Select value={c.op} onChange={(v) => setCondition(i, { op: v })}>
                    {meta?.operators.map((o) => (
                      <option key={o} value={o}>
                        {o}
                      </option>
                    ))}
                  </Select>
                  <input
                    type="number"
                    value={c.value}
                    onChange={(e) => setCondition(i, { value: Number(e.target.value) })}
                    className="w-28 rounded-md border border-input bg-background px-2 py-1.5 text-sm outline-none focus:border-ring"
                  />
                  <Button kind="ghost" onClick={() => removeCondition(i)}>
                    移除
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>

        {problemText && (
          <p className="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning">
            {problemText}
          </p>
        )}
      </div>
    </Modal>
  )
}
