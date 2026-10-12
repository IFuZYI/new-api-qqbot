/** Typed client for the qqbot API. */

export interface NewAPIConfig {
  url: string
  username: string
  password: string
}

export interface NapCatConfig {
  webui_url: string
  webui_token: string
  onebot_url: string
  onebot_token: string
  event_ws_url: string
  event_ws_token: string
}

export interface Config {
  newapi: NewAPIConfig
  napcat: NapCatConfig
}

/** Per-section revisions, so the two connection cards are guarded separately. */
export interface ConfigRevisions {
  newapi: string
  napcat: string
}

export interface ConfigResponse {
  config: Config
  revision: string
  revisions: ConfigRevisions
}

/** What NapCat auto-configuration wired up. */
export interface NapCatAdapterResult {
  token: string
  onebot_url: string
  event_ws_url: string
}

/** One transport's outcome from a connection test. */
export interface CheckResult {
  name: string
  ok: boolean
  detail?: string
  error?: string
  skipped?: boolean
}

export interface Settings {
  // Patrols
  member_patrol_enabled: boolean
  member_sync_minutes: number
  checkin_patrol_enabled: boolean
  checkin_sync_minutes: number

  // Binding paths. Only ever touches members with no binding.
  bind_command_enabled: boolean
  /** Command list, one pattern per line; /slash-delimited/ lines are regexes. */
  bind_command_patterns: string
  bind_on_join_enabled: boolean
  bind_patrol_enabled: boolean
  bind_patrol_minutes: number

  // Check-in paths
  checkin_by_message_enabled: boolean
  checkin_patterns: string

  // Reward. Values are in new-api's own quota unit (the gateway's add_quota
  // adds straight to the account balance); the panel never converts them.
  reward_min: number
  reward_max: number
  timezone: string
}

/** One patrol's health, as the scheduler reports it. */
export interface PatrolState {
  last_run?: number
  last_error?: string
}

export interface StatusResponse {
  patrols: Record<string, PatrolState>
  settings: Settings
}

export interface Group {
  group_id: number
  group_name: string
  member_count: number
  enabled: boolean
  updated_at: number
}

export interface Member {
  qq: number
  nickname: string
  group_ids: number[] | null
  bound: boolean
  newapi_user_id?: number
  newapi_username?: string
  newapi_email?: string
  bind_source?: string
  checked_today: boolean
  checkin_days: number
  reward_today: number
  /** Reward lifecycle: pending | claimed | ambiguous | paid | legacy. */
  reward_state?: string
  /** Why a reward is not settled; present for ambiguous rows. */
  reward_error?: string
}

export interface MembersResponse {
  items: Member[]
  total: number
  date: string
}

/** One row of the user directory: a QQ member, a left member, or a lone account. */
export interface UserRow {
  kind: 'member' | 'left' | 'account'
  qq: number
  nickname: string
  group_ids: number[] | null
  in_group: boolean

  bound: boolean
  newapi_user_id?: number
  newapi_username?: string
  newapi_email?: string
  newapi_group?: string
  newapi_status?: number
  bind_source?: string

  checked_today: boolean
  /** How today's check-in was learned: board or message. Absent when none. */
  checkin_source?: 'board' | 'message'
  checkin_days: number
  reward_today: number
  /** Reward lifecycle: pending | claimed | ambiguous | paid | legacy. */
  reward_state?: string
  /** Why a reward is not settled; present for ambiguous rows. */
  reward_error?: string
}

export interface UserSummary {
  total: number
  bound: number
  unbound: number
  in_group: number
  left: number
  accounts: number
  not_joined: number
}

export interface UsersResponse {
  items: UserRow[]
  total: number
  date: string
  summary: UserSummary
}

export interface UserFilter {
  bound?: '' | 'bound' | 'unbound'
  in_group?: '' | 'in' | 'out'
  group?: string
  q?: string
  kind?: '' | 'member' | 'left' | 'account'
}

export interface GroupChangeResult {
  user_id: number
  username?: string
  qq?: number
  group: string
  ok: boolean
  error?: string
}

export interface Binding {
  qq: number
  newapi_user_id: number
  newapi_username: string
  newapi_email: string
  source: string
  created_at: number
  updated_at: number
}

export interface Bootstrap {
  configured: boolean
  settings: Settings
  groups: Group[] | null
}

/**
 * Every panel request is bounded by this. 30s is the budget the backends give
 * their own upstream calls (internal/newapi/client.go and
 * internal/napcat/client.go both build an http.Client with a 30s timeout), so
 * the panel must not give up earlier: if it did, a slow gateway call would be
 * reported as a panel-side timeout instead of the backend's own, more specific
 * error. A request that outlives 30s is a hang — without this the panel spins
 * forever with no feedback, so the operator is told instead.
 */
const REQUEST_TIMEOUT_MS = 30_000

/** Timeout wording: it must not read like a network failure. */
const TIMEOUT_MESSAGE =
  '请求超时：30 秒内未收到后端响应，已中断。通常是后端或上游服务（new-api / NapCat）卡住，而不是网络断开。'

export const SESSION_EXPIRED = 'qqbot:session-expired'

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const ctl = new AbortController()
  // Distinguishes our deadline from a caller-side cancellation: both surface
  // as AbortError, and only ours may be reported as a timeout.
  let timedOut = false
  const timer = setTimeout(() => {
    timedOut = true
    ctl.abort()
  }, REQUEST_TIMEOUT_MS)
  // A caller-supplied signal (none today, but RequestInit allows one) is
  // forwarded rather than dropped: the fetch below uses our controller, so the
  // caller's abort is relayed to it.
  const caller = init?.signal
  const relay = () => ctl.abort()
  if (caller) {
    if (caller.aborted) ctl.abort()
    else caller.addEventListener('abort', relay, { once: true })
  }
  try {
    const res = await fetch(path, {
      ...init,
      signal: ctl.signal,
      headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
    })
    // Authentication endpoints may reject incorrect credentials without expiring
    // an existing panel session. All protected endpoints share this boundary.
    if (res.status === 401 && !['/api/auth/state', '/api/auth/login', '/api/auth/setup', '/api/auth/logout'].includes(path)) {
      window.dispatchEvent(new Event(SESSION_EXPIRED))
    }
    const text = await res.text()
    let body: unknown = null
    try {
      body = text ? JSON.parse(text) : null
    } catch {
      throw new Error(`${res.status}: ${text.slice(0, 200)}`)
    }
    if (!res.ok) {
      const msg = (body as { error?: string })?.error ?? `HTTP ${res.status}`
      throw new Error(msg)
    }
    return body as T
  } catch (e) {
    if (timedOut && e instanceof Error && e.name === 'AbortError') {
      throw new Error(TIMEOUT_MESSAGE)
    }
    throw e
  } finally {
    // Without this every request would leave a pending timer behind.
    clearTimeout(timer)
    caller?.removeEventListener('abort', relay)
  }
}

export interface Condition {
  type: string
  op: string
  value: number
}

export interface Rule {
  id: number
  name: string
  enabled: boolean
  on_bind: boolean
  interval_minutes: number
  source_groups: string[]
  target_group: string
  conditions: Condition[]
  last_run_at: number
  last_error?: string
  // Set when the stored rule could not be read back. Such a rule matches
  // nobody, so it must be shown as broken rather than as a working rule with
  // its lists silently emptied.
  decode_error?: string
}

export interface RuleMeta {
  condition_types: { value: string; label: string }[]
  operators: string[]
}

export interface AuthState {
  needs_setup: boolean
  authenticated: boolean
  username: string
}

export const api = {
  authState: () => req<AuthState>('/api/auth/state'),
  setup: (username: string, password: string) =>
    req<{ username: string }>('/api/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  login: (username: string, password: string) =>
    req<{ username: string }>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logout: () => req<{ ok: boolean }>('/api/auth/logout', { method: 'POST', body: '{}' }),
  changePassword: (current: string, next: string) =>
    req<{ ok: boolean }>('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current, next }),
    }),

  bootstrap: () => req<Bootstrap>('/api/bootstrap'),
  getConfig: () => req<ConfigResponse>('/api/config'),
  saveConfig: (c: Config, revision: string) =>
    req<ConfigResponse>('/api/config', {
      method: 'POST',
      body: JSON.stringify({ config: c, revision }),
    }),
  /** Save only the gateway half; the NapCat card is left untouched. */
  saveNewAPIConfig: (c: Config, revision: string) =>
    req<ConfigResponse>('/api/config/newapi', {
      method: 'POST',
      body: JSON.stringify({ config: { newapi: c.newapi }, revision }),
    }),
  /** Save only the NapCat half. */
  saveNapCatConfig: (c: Config, revision: string) =>
    req<ConfigResponse>('/api/config/napcat', {
      method: 'POST',
      body: JSON.stringify({ config: { napcat: c.napcat }, revision }),
    }),
  /**
   * Provision NapCat's OneBot adapters from just its WebUI address, then store
   * the resulting OneBot/event URLs and token.
   */
  autoConfigureNapCat: (body: {
    webui_url: string
    webui_token: string
    revision: string
    http_port?: number
    ws_port?: number
    bind_host?: string
  }) =>
    req<ConfigResponse & { napcat: NapCatAdapterResult }>('/api/config/napcat/autoconfigure', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  /** Test the gateway credentials; returns one result per probed hop. */
  testNewAPI: (body: { url: string; username: string; password: string }) =>
    req<{ results: CheckResult[] }>('/api/config/test/newapi', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  /** Test each configured NapCat transport independently. */
  testNapCat: (body: {
    webui_url: string
    webui_token: string
    onebot_url: string
    onebot_token: string
    event_ws_url: string
    event_ws_token: string
  }) =>
    req<{ results: CheckResult[] }>('/api/config/test/napcat', {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  groups: () => req<{ items: Group[] }>('/api/groups'),
  refreshGroups: () => req<Group[]>('/api/groups/refresh', { method: 'POST', body: '{}' }),
  saveGroups: (ids: number[]) =>
    req<Group[]>('/api/groups', { method: 'POST', body: JSON.stringify({ group_ids: ids }) }),

  members: () => req<MembersResponse>('/api/members'),

  users: (f: UserFilter = {}) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(f)) {
      if (v !== undefined && v !== '') q.set(k, String(v))
    }
    const qs = q.toString()
    return req<UsersResponse>(`/api/users${qs ? `?${qs}` : ''}`)
  },
  syncUsers: () => req<{ accounts: number }>('/api/users/sync', { method: 'POST', body: '{}' }),
  setUserGroup: (id: number, group: string) =>
    req<{ ok: boolean; newapi_user_id: number; group: string }>(`/api/users/${id}/group`, {
      method: 'POST',
      body: JSON.stringify({ group }),
    }),
  setUserGroups: (userIds: number[], group: string) =>
    req<{ results: GroupChangeResult[]; succeeded: number; failed: number }>('/api/users/groups', {
      method: 'POST',
      body: JSON.stringify({ user_ids: userIds, group }),
    }),

  bindings: () => req<{ items: Binding[] }>('/api/bindings'),
  createBinding: (b: { qq: number; newapi_user_id: number; newapi_username?: string; newapi_email?: string }) =>
    req<{ binding: Binding; rules: { rule_id: number; rule_name: string; promoted: number; skipped: number; failed: number; error?: string }[]; rule_error?: string }>('/api/bindings', { method: 'POST', body: JSON.stringify(b) }),
  deleteBinding: (qq: number) =>
    req<{ ok: boolean }>(`/api/bindings/${qq}/delete`, { method: 'POST', body: '{}' }),

  settings: () => req<Settings>('/api/settings'),
  saveSettings: (s: Settings) =>
    req<Settings>('/api/settings', { method: 'POST', body: JSON.stringify(s) }),
  /** Patrol health plus the switches, so the UI can tell stopped from broken. */
  status: () => req<StatusResponse>('/api/status'),

  rules: () => req<{ items: Rule[] }>('/api/rules'),
  ruleMeta: () => req<RuleMeta>('/api/rules/meta'),
  createRule: (r: Rule) => req<Rule>('/api/rules', { method: 'POST', body: JSON.stringify(r) }),
  updateRule: (r: Rule) =>
    req<Rule>(`/api/rules/${r.id}`, { method: 'PUT', body: JSON.stringify(r) }),
  deleteRule: (id: number) =>
    req<{ ok: boolean }>(`/api/rules/${id}/delete`, { method: 'POST', body: '{}' }),

  /** new-api group names, for the source/target pickers. */
  newAPIGroups: () => req<Record<string, { desc: string; ratio: number }>>('/api/newapi/groups'),

  /** Run a named sweep on the server. */
  action: (name: string, body: Record<string, unknown> = {}) =>
    req<Record<string, unknown>>(`/api/actions/${name}`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  /**
   * Run one patrol now, from the 测试一次 button. It is the same sweep the timer
   * runs (and it is recorded), so the row's health badge updates afterwards.
   */
  runPatrol: (name: string) =>
    req<{ patrol: string; result: unknown }>(`/api/patrols/${name}/run`, {
      method: 'POST',
      body: '{}',
    }),
}
