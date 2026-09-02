/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Boxes, Plus, RefreshCw, X, Zap } from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'

import type { PcUnifiedRow } from '../types'
import type { PriceCompareState } from '../use-price-compare'

// 与后端 pcKeyVault 字段一一对应
const KEY_GROUPS = [
  { key: 'openrouter', labelKey: 'OpenRouter keys' },
  { key: 'opencode', labelKey: 'OpenCode Go keys' },
  { key: 'deepseek', labelKey: 'DeepSeek official keys' },
  { key: 'apib', labelKey: 'APIB keys' },
  { key: 'buzzai', labelKey: 'BuzzAI keys' },
  { key: 'apikl', labelKey: 'APIKL keys' },
  { key: 'ikun', labelKey: 'IKUN keys' },
] as const

type VaultState = Record<(typeof KEY_GROUPS)[number]['key'], string[]>

type KeyConflict = { id: number; name: string }

// 后端 GetPriceCompareBalances 的每服务商结果
type PcBalance = BalLike & {
  currency?: string
  keys?: PcKeyBalance[] // 逐 key 明细（服务端按保险箱顺序返回，前端按掩码 key 关联）
}

// tooltip/错误文案共用字段（聚合结果与单条 key 结构相同）
type BalLike = {
  ok: boolean
  balance?: number
  used?: number
  granted?: number
  windows?: Record<string, number>
  error?: string
  detail?: string
  balance_cny?: number
  used_cny?: number
  // sub2api 附加（apikl/ikun 单条 key）：组名 / 组倍率 / 近期调用模型
  plan?: string
  group_rate?: number
  models?: string[]
}

// 单条 key 的余额明细（key 为服务端脱敏掩码）
type PcKeyBalance = BalLike & {
  key: string
  currency?: string
}

// /v1/models 逐 key 探测结果（该 key 实际可见/可调的模型集合）
type PcKeyAccess = {
  key: string
  ok: boolean
  count?: number
  models?: string[]
  error?: string
  detail?: string
}

// 脱敏展示：开头与末尾各 6 位，中间星号
const maskKey = (k: string) => {
  if (k.length > 15) return `${k.slice(0, 6)}******${k.slice(-6)}`
  if (k.length > 8) return `${k.slice(0, 4)}****${k.slice(-3)}`
  return '******'
}

// GET /keys 响应（服务端 vault 原文 + 渠道检测数）
type KeysResponse = Partial<VaultState> & { detected?: Record<string, number> }

// 旧库无比价数据时各组回退空数组（优雅空态）
const vaultFrom = (d: KeysResponse): VaultState => ({
  openrouter: d.openrouter ?? [],
  opencode: d.opencode ?? [],
  deepseek: d.deepseek ?? [],
  apib: d.apib ?? [],
  buzzai: d.buzzai ?? [],
  apikl: d.apikl ?? [],
  ikun: d.ikun ?? [],
})

// 取服务端最新 vault；失败时抛错（调用方据此放弃覆盖式保存）
const fetchKeys = () =>
  api
    .get('/api/price_compare/keys')
    .then((res) => {
      if (!res.data.success) throw new Error(res.data?.message || 'request failed')
      return (res.data.data ?? {}) as KeysResponse
    })

// 行情行的价格字段（可调模型弹窗按 key 组倍率换算实付价用）由父组件 pc.rows 提供
export function KeysCard({ pc }: { pc: PriceCompareState }) {
  const { t } = useTranslation()
  const [vault, setVault] = useState<VaultState>({
    openrouter: [],
    opencode: [],
    deepseek: [],
    apib: [],
    buzzai: [],
    apikl: [],
    ikun: [],
  })
  const [saving, setSaving] = useState(false)
  // 密钥列表加载失败（≠ 真空态）：显示错误态并禁用保存类操作，避免误导
  const [loadError, setLoadError] = useState(false)
  const [addFor, setAddFor] = useState<(typeof KEY_GROUPS)[number]['key'] | null>(null)
  const [addVal, setAddVal] = useState('')
  const [conflicts, setConflicts] = useState<KeyConflict[]>([])
  const [purging, setPurging] = useState(false)
  // 现有渠道里检测到、尚未导入保险箱的密钥数（kind → 数量）
  const [detected, setDetected] = useState<Record<string, number>>({})
  const [importing, setImporting] = useState(false)
  // 三家余额（保险箱密钥 → 官方 API，后端 60s 缓存）
  const [balances, setBalances] = useState<Record<string, PcBalance>>({})
  const [balLoading, setBalLoading] = useState(false)
  // 逐 key 可调模型探测（/v1/models 免费，后端 10min 缓存）
  const [access, setAccess] = useState<Record<string, { keys: PcKeyAccess[] }>>({})
  const [probing, setProbing] = useState(false)
  // 可调模型弹窗：当前查看的渠道 + 掩码 key（与探测结果按掩码关联）
  const [accessFor, setAccessFor] = useState<{ kind: string; key: string } | null>(null)
  const [modelFilter, setModelFilter] = useState('')
  // 价格索引（channel|ref → 最低输入价行，Period/Quant 变体取最便宜者），
  // 直接复用父组件已加载的行情行，不重复拉取
  const priceIdx = useMemo(() => {
    const m = new Map<string, PcUnifiedRow>()
    for (const r of pc.rows) {
      if (!r.channel || !r.ref || !r.in) continue
      const k = `${r.channel}|${r.ref}`
      const prev = m.get(k)
      if (!prev || r.in < prev.in) m.set(k, r)
    }
    return m
  }, [pc.rows])

  const loadKeys = () => {
    fetchKeys()
      .then((d) => {
        setVault(vaultFrom(d))
        setDetected(d.detected ?? {})
        setLoadError(false)
      })
      .catch(() => setLoadError(true)) // 失败不能伪装成「没有 key」的空态
  }

  useEffect(() => {
    loadKeys()
  }, [])

  const loadBalances = (force = false) => {
    setBalLoading(true)
    api
      .get(`/api/price_compare/keys/balance${force ? '?refresh=1' : ''}`)
      .then((res) => {
        if (res.data.success) setBalances(res.data.data ?? {})
      })
      .finally(() => setBalLoading(false))
      .catch(() => undefined)
  }

  useEffect(() => {
    loadBalances()
  }, [])

  const loadAccess = (force = false) => {
    setProbing(true)
    api
      .get(`/api/price_compare/keys/access${force ? '?refresh=1' : ''}`)
      .then((res) => {
        if (res.data.success) setAccess(res.data.data ?? {})
      })
      .finally(() => setProbing(false))
      .catch(() => undefined)
  }

  useEffect(() => {
    loadAccess()
  }, [])

  // 余额徽章文案：openrouter/deepseek 显示人民币金额，opencode 显示 Go 订阅窗口用量，
  // 中转站类目无公开余额 API 显示 —
  const balText = (kind: (typeof KEY_GROUPS)[number]['key'], b?: PcBalance) => {
    if (!b) return '—' // 加载中/无数据统一显示 —（刷新按钮转圈表示进行中）
    if (!b.ok) return '—'
    if (kind === 'opencode' && b.windows) {
      const w = b.windows
      const pct = (v?: number) => (v == null ? '–' : `${Math.round(v)}%`)
      return `5h ${pct(w.rolling)} · 7d ${pct(w.weekly)} · 30d ${pct(w.monthly)}`
    }
    if (b.balance != null) {
      if (kind === 'openrouter') return b.balance_cny != null ? `¥${b.balance_cny.toFixed(2)}` : `$${b.balance.toFixed(2)}`
      return `${b.currency === 'USD' ? '$' : '¥'}${b.balance.toFixed(2)}`
    }
    if (b.used != null) {
      // 网关报「不限额」时只有已用值
      return `${t('Used')}${b.currency === 'USD' ? '$' : '¥'}${b.used.toFixed(2)}`
    }
    return '—'
  }

  const balTitle = (b?: BalLike) => {
    if (!b) return t('Balances are fetched from official provider APIs using the vault keys')
    if (!b.ok) {
      let code = t('Request failed')
      if (b.error === 'no_key') code = t('No key configured')
      else if (b.error === 'invalid_key') code = t('Invalid key')
      else if (b.error === 'no_go_sub') code = t('No Go subscription (the Zen wallet has no official balance API)')
      return b.detail ? `${code} (${b.detail})` : code
    }
    const parts: string[] = []
    if (b.balance_cny != null && b.balance != null) parts.push(`USD: $${b.balance.toFixed(2)}`)
    if (b.used != null) {
      const u =
        b.used_cny != null ? `¥${b.used_cny.toFixed(2)} ($${b.used.toFixed(2)})` : `$${b.used.toFixed(2)}`
      parts.push(`${t('Used')}: ${u}`)
    }
    if (b.granted != null) parts.push(`${t('Granted')}: ¥${b.granted.toFixed(2)}`)
    if (b.windows) {
      const w = b.windows
      const pct = (v?: number) => (v == null ? '–' : `${Math.round(v)}%`)
      parts.push(`5h ${pct(w.rolling)} · 7d ${pct(w.weekly)} · 30d ${pct(w.monthly)}`)
    }
    // sub2api（apikl/ikun）：组名 + 该 key 的组倍率 + 近期调用过的模型——
    // 同站多 key 常各限特定模型（如 ikun 一个 key 只能生图、另一个只能 GPT 组）
    if (b.plan) parts.push(b.plan)
    if (b.group_rate != null) parts.push(`×${Number(b.group_rate.toFixed(2))}`)
    if (b.models?.length) parts.push(b.models.join(', '))
    if (b.detail) parts.push(b.detail)
    return parts.join(' · ') || t('Balances are fetched from official provider APIs using the vault keys')
  }

  // 单条 key 的 chip 尾缀：人民币金额 / Go 窗口用量 / ✗ 失效；没钱（≤0 或窗口 100%）标红。
  // sub2api key 附带组倍率（×1 / ×1.5…）内联展示，让「哪条 key 属于哪个组」一眼可辨
  const keySuffix = (kind: (typeof KEY_GROUPS)[number]['key'], kb?: PcKeyBalance) => {
    if (!kb) {
      return {
        text: '—',
        bad: false,
        title: t('Balances are fetched from official provider APIs using the vault keys'),
      }
    }
    if (!kb.ok) {
      if (kb.error === 'no_key') return null
      return { text: '✗', bad: true, title: balTitle(kb) }
    }
    if (kind === 'opencode') {
      if (!kb.windows) return null
      const w = kb.windows
      const pct = (v?: number) => (v == null ? '–' : `${Math.round(v)}%`)
      return {
        // 半宽 chip 放不下 " · "（会截断成 30d 1…），间隔点紧凑、标签与数值间保留空格
        text: `5h ${pct(w.rolling)}·7d ${pct(w.weekly)}·30d ${pct(w.monthly)}`,
        bad: [w.rolling, w.weekly, w.monthly].some((v) => (v ?? 0) >= 100),
        title: balTitle(kb),
      }
    }
    const rate =
      kb.group_rate != null ? ` · ×${Number(kb.group_rate.toFixed(2))}` : ''
    if (kb.balance == null) {
      if (kb.used == null) return null
      const sym = kb.currency === 'USD' ? '$' : '¥'
      return { text: `${t('Used')}${sym}${kb.used.toFixed(2)}${rate}`, bad: false, title: balTitle(kb) }
    }
    const v = kb.balance_cny ?? kb.balance
    if (v == null) return null
    // DeepSeek 官方 CNY 计价但 per-key 未带货币标识（与组级聚合 chip 的 ¥ 一致）
    const sym =
      kb.currency === 'CNY' || kb.balance_cny != null || kind === 'deepseek'
        ? '¥'
        : '$'
    return {
      text: `${sym}${v.toFixed(2)}${rate}`,
      bad: v <= 0,
      title: balTitle(kb),
    }
  }

  const detectedTotal = Object.values(detected).reduce((a, b) => a + b, 0)

  // 一键把现有同名渠道里的密钥导入保险箱（免去查看渠道密钥原文的限流端点）
  const importDetected = async () => {
    setImporting(true)
    try {
      const res = await api.post('/api/price_compare/keys/import')
      const d = res.data?.data ?? {}
      const parts = Object.entries(d.imported ?? {})
        .filter(([, n]) => (n as number) > 0)
        .map(([k, n]) => `${k} ${n}`)
      toast.success(t('{{count}} keys imported from existing channels', { count: d.total ?? 0 }) + (parts.length ? ` (${parts.join(' · ')})` : ''))
      if ((d.conflicts ?? 0) > 0) {
        toast.info(t('{{count}} duplicate-key channels can be cleaned up', { count: d.conflicts }))
      }
      loadKeys()
      loadBalances(true)
      loadAccess(true)
    } catch {
      toast.error(t('Request failed'))
    } finally {
      setImporting(false)
    }
  }

  // 保存即同步进名称匹配的渠道（唯一 key 录入点），并返回同 key 冲突渠道。
  // 保险箱是全量覆盖式保存：先把本次增删应用到重取的服务端最新值上再提交，
  // 防止本地陈旧态（GET 失败回退的空态、多标签页并发编辑）把其余组已存 key
  // 整体覆盖掉（中转组 key 无渠道副本，覆盖即丢失）；取不到服务端值就放弃保存
  const mutateKeys = async (
    group: (typeof KEY_GROUPS)[number]['key'],
    apply: (list: string[]) => string[]
  ) => {
    const prev = vault
    setSaving(true)
    try {
      const d = await fetchKeys()
      const next = { ...vaultFrom(d), [group]: apply(d[group] ?? []) }
      setVault(next)
      setDetected(d.detected ?? {})
      const res = await api.post('/api/price_compare/keys', next)
      setLoadError(false) // 全量保存成功即证明加载已恢复：解除错误横幅与保存禁用
      const n = res.data?.data?.channels ?? 0
      const cf = (res.data?.data?.conflicts ?? []) as KeyConflict[]
      if (cf.length > 0) {
        setConflicts(cf) // 同 key 的非管理渠道：弹窗让用户确认删除接管
      } else {
        toast.success(t('{{count}} channels synced', { count: n }))
      }
      loadBalances(true) // key 集变了，立即重取逐 key 余额
      loadAccess(true)
    } catch {
      setVault(prev) // 保存失败回滚，界面不与服务端脱节
      toast.error(t('Failed to save settings'))
    } finally {
      setSaving(false)
    }
  }

  const confirmAdd = () => {
    const k = addVal.trim()
    if (!k || !addFor) return
    // 已在保险箱的 key 不重复追加（服务端会去重，重复会让本地多出余额恒为 — 的幽灵 chip）
    void mutateKeys(addFor, (list) => (list.includes(k) ? list : [...list, k]))
    setAddVal('')
    setAddFor(null)
  }

  // 按 key 值删除：基底取自服务端最新值，本地列表下标可能已失真
  const removeKey = (group: (typeof KEY_GROUPS)[number]['key'], k: string) => {
    void mutateKeys(group, (list) => list.filter((x) => x !== k))
  }

  const purgeConflicts = async () => {
    setPurging(true)
    try {
      const res = await api.post('/api/price_compare/keys/purge', {
        ids: conflicts.map((c) => c.id),
      })
      const n = res.data?.data?.deleted ?? 0
      toast.success(t('{{count}} duplicate channels deleted', { count: n }))
      setConflicts([])
    } catch {
      toast.error(t('Request failed'))
    } finally {
      setPurging(false)
    }
  }

  const addGroupLabel =
    KEY_GROUPS.find((g) => g.key === addFor)?.labelKey ?? ''

  // 弹窗数据：按掩码 key 定位探测结果，模型字母序排列后按筛选词过滤
  const accessEntry = accessFor
    ? access[accessFor.kind]?.keys?.find((a) => a.key === accessFor.key)
    : null
  const accessModels = (accessEntry?.models ?? [])
    .filter((m) =>
      modelFilter ? m.toLowerCase().includes(modelFilter.toLowerCase()) : true
    )
    .sort((a, b) => a.localeCompare(b))

  // 这把 key 的组倍率（sub2api 系余额接口逐 key 提供；其他类目无分组概念）
  const accessKeyRate = accessFor
    ? balances[accessFor.kind]?.keys?.find((b) => b.key === accessFor.key)
        ?.group_rate
    : undefined

  // 组价换算：行情行是市场最低组价（group_rate=行内倍率），这把 key 的实付
  // = 行价 × key组倍率 / 行组倍率；无分组数据的模型不显示价格
  const keyPrice = (
    m: string
  ): { text: string; title: string } | null => {
    const row = accessFor ? priceIdx.get(`${accessFor.kind}|${m}`) : undefined
    if (!row || !row.in) return null
    const fmt = (n: number) => (n >= 1 ? n.toFixed(2) : n.toFixed(4))
    const market = row.group_rate ?? 0
    const adj = accessKeyRate != null && market > 0 && accessKeyRate > 0
    const f = adj ? (accessKeyRate as number) / market : 1
    const inP = fmt(row.in * f)
    const outP = fmt(row.out * f)
    return {
      text: `¥${inP}/M`,
      title: adj
        ? t(
            'Per 1M tokens at this key group rate ×{{rate}} (lowest group ×{{market}}): input ¥{{in}}, output ¥{{out}}',
            { rate: accessKeyRate, market, in: inP, out: outP }
          )
        : t('Per 1M tokens: input ¥{{in}}, output ¥{{out}}', {
            in: inP,
            out: outP,
          }),
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className='flex items-start justify-between gap-2'>
          <div>
            <CardTitle>{t('Channel API keys')}</CardTitle>
            <CardDescription>
              {t(
                'The single source of channel keys — saved keys sync into matching new-api channels here and on every auto update; the OpenRouter channel routes cheapest provider first (provider.sort=price)'
              )}
            </CardDescription>
          </div>
          <div className='flex shrink-0 items-center gap-1.5'>
            <Button
              size='sm'
              variant='outline'
              className='h-7'
              disabled={probing}
              title={t('Probe accessible models (free /v1/models call)')}
              onClick={() => loadAccess(true)}
            >
              <Zap className={`mr-1 h-3 w-3${probing ? ' animate-spin' : ''}`} />
              {t('Probe models')}
            </Button>
            <Button
              size='sm'
              variant='outline'
              className='h-7'
              disabled={balLoading}
              title={t('Refresh balances')}
              onClick={() => loadBalances(true)}
            >
              <RefreshCw className={`mr-1 h-3 w-3${balLoading ? ' animate-spin' : ''}`} />
              {t('Refresh balances')}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className='space-y-3'>
        {loadError && (
          <div className='flex flex-wrap items-center gap-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs text-destructive'>
            <span>{t('Failed to load API keys')}</span>
            <Button
              size='sm'
              variant='outline'
              className='ml-auto h-7'
              onClick={loadKeys}
            >
              {t('Retry')}
            </Button>
          </div>
        )}
        {detectedTotal > 0 && (
          <div className='flex flex-wrap items-center gap-2 rounded-md border border-dashed bg-muted/30 px-3 py-2 text-xs'>
            <span className='text-muted-foreground'>
              {t('{{count}} keys found in existing (non-managed) channels — import them here so the panel can take over channel sync', { count: detectedTotal })}
            </span>
            <Button
              size='sm'
              variant='outline'
              className='ml-auto h-7'
              disabled={importing || loadError}
              onClick={() => void importDetected()}
            >
              {importing ? t('Importing…') : t('Import keys')}
            </Button>
          </div>
        )}
        <div className='grid grid-cols-1 gap-3 lg:grid-cols-3'>
          {KEY_GROUPS.map((g) => {
            const keys = vault[g.key] ?? []
            const bal = balances[g.key]
            return (
              <div key={g.key} className='rounded-lg border p-3'>
                <div className='mb-2 flex items-center justify-between gap-1.5'>
                  <span className='text-xs font-medium'>{t(g.labelKey)}</span>
                  <div className='flex items-center gap-1.5'>
                    <span
                      title={balTitle(bal)}
                      className={`inline-flex items-center rounded-md border px-1.5 py-0.5 text-xs tabular-nums ${
                        bal && !bal.ok && bal.error !== 'no_key'
                          ? 'text-destructive border-destructive/40 bg-destructive/5'
                          : 'bg-muted/30 text-foreground'
                      }`}
                    >
                      {balText(g.key, bal)}
                    </span>
                    <Badge variant='secondary'>{keys.length}</Badge>
                  </div>
                </div>
                <div className='flex flex-col gap-1.5'>
                  {keys.length === 0 ? (
                    <span className='text-muted-foreground text-xs'>
                      {t('No keys yet')}
                    </span>
                  ) : (
                    keys.map((k, i) => {
                      // 余额/探测明细均按掩码 key 关联（后端 pcMaskKey 与前端同算法）；
                      // 不按下标取——保存后到余额重取返回前本地数组已重排，下标会错位
                      const s = keySuffix(
                        g.key,
                        bal?.keys?.find((b) => b.key === maskKey(k))
                      )
                      const acc = access[g.key]?.keys?.find((a) => a.key === maskKey(k))
                      return (
                        <div key={`${i}-${k.slice(-4)}`} className='grid grid-cols-2 gap-1.5'>
                          {/* 左半：key 掩码 chip（文字居中，删除钮悬停浮现在右缘） */}
                          <span className='group relative flex min-w-0 items-center justify-center rounded-md border bg-muted/30 px-2 py-0.5 font-mono text-xs'>
                            <span className='truncate'>{maskKey(k)}</span>
                            <button
                              type='button'
                              tabIndex={-1}
                              aria-label={t('Delete') as string}
                              disabled={loadError}
                              className='absolute right-1 text-muted-foreground opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100'
                              onClick={() => removeKey(g.key, k)}
                            >
                              <X className='h-3 w-3' />
                            </button>
                          </span>
                          {/* 右半：余额 chip + 可调模型按钮（弹窗 chip 呈现，字母序） */}
                          <span className='flex min-w-0 items-center gap-1'>
                            <span
                              title={s?.title}
                              className={`flex min-w-0 flex-1 items-center justify-center rounded-md border px-2 py-0.5 text-xs tabular-nums ${
                                s?.bad
                                  ? 'border-destructive/40 bg-destructive/5 text-destructive'
                                  : 'bg-muted/30 text-foreground'
                              }`}
                            >
                              <span className='truncate'>{s ? s.text : '—'}</span>
                            </span>
                            {acc && (
                              <button
                                type='button'
                                tabIndex={-1}
                                title={
                                  acc.ok
                                    ? `${t('Accessible models')}${acc.count != null ? ` (${acc.count})` : ''}`
                                    : `${acc.error || ''} ${acc.detail || ''}`.trim()
                                }
                                aria-label={t('Accessible models') as string}
                                className={`inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-md border ${
                                  acc.ok
                                    ? 'bg-muted/30 text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground'
                                    : 'border-destructive/40 bg-destructive/5 text-destructive'
                                }`}
                                onClick={() => {
                                  setModelFilter('')
                                  setAccessFor({ kind: g.key, key: acc.key })
                                }}
                              >
                                <Boxes className='h-3 w-3' />
                              </button>
                            )}
                          </span>
                        </div>
                      )
                    })
                  )}
                </div>
                <Button
                  size='sm'
                  variant='outline'
                  className='mt-2 h-7 w-full'
                  disabled={loadError}
                  onClick={() => {
                    setAddFor(g.key)
                    setAddVal('')
                  }}
                >
                  <Plus className='mr-1 h-3 w-3' />
                  {t('Add key')}
                </Button>
              </div>
            )
          })}
        </div>
        {saving && (
          <div className='text-muted-foreground text-right text-xs'>
            {t('Saving…')}
          </div>
        )}
      </CardContent>

      {/* 添加 key 弹窗 */}
      <Dialog
        open={addFor != null}
        onOpenChange={(v) => {
          if (!v) setAddFor(null)
        }}
      >
        <DialogContent className='sm:max-w-sm'>
          <DialogHeader>
            <DialogTitle>
              {t('Add key')}
              {addGroupLabel ? ` · ${t(addGroupLabel)}` : ''}
            </DialogTitle>
            <DialogDescription>
              {t(
                'The key is masked in the list and synced into matching channels after saving'
              )}
            </DialogDescription>
          </DialogHeader>
          <Input
            autoFocus
            value={addVal}
            placeholder={t('Paste API key') as string}
            className='font-mono text-xs'
            onChange={(e) => setAddVal(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') confirmAdd()
            }}
          />
          <div className='flex justify-end gap-2'>
            <Button
              size='sm'
              variant='outline'
              onClick={() => setAddFor(null)}
            >
              {t('Cancel')}
            </Button>
            <Button size='sm' disabled={!addVal.trim()} onClick={confirmAdd}>
              {t('Save')}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {/* 同 key 冲突渠道确认删除 */}
      <Dialog
        open={conflicts.length > 0}
        onOpenChange={(v) => {
          if (!v) setConflicts([])
        }}
      >        <DialogContent className='sm:max-w-md'>
          <DialogHeader>
            <DialogTitle>{t('Duplicate key channels detected')}</DialogTitle>
            <DialogDescription>
              {t(
                'These channels are not panel-managed but use the same API keys. Delete them so the managed channels take over?'
              )}
            </DialogDescription>
          </DialogHeader>
          <div className='max-h-40 overflow-auto rounded-md border p-2'>
            {conflicts.map((c) => (
              <div key={c.id} className='text-muted-foreground px-1 py-0.5 text-xs'>
                #{c.id} · {c.name}
              </div>
            ))}
          </div>
          <div className='flex items-center justify-end gap-2'>
            <Button
              size='sm'
              variant='outline'
              onClick={() => setConflicts([])}
              disabled={purging}
            >
              {t('Keep')}
            </Button>
            <Button
              size='sm'
              variant='destructive'
              disabled={purging}
              onClick={() => void purgeConflicts()}
            >
              {t('Delete channels')}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {/* 可调模型弹窗：chip 卡片按字母序呈现，带筛选（OpenRouter 单 key 可达 458 个） */}
      <Dialog
        open={accessFor != null}
        onOpenChange={(v) => {
          if (!v) {
            setAccessFor(null)
            setModelFilter('')
          }
        }}
      >
        <DialogContent className='sm:max-w-lg'>
          <DialogHeader>
            <DialogTitle>
              {t('Accessible models')}
              {accessEntry?.ok && accessEntry.count != null ? ` · ${accessEntry.count}` : ''}
            </DialogTitle>
            <DialogDescription>
              {accessFor
                ? `${accessFor.key} · ${t('Free /v1/models probe, sorted alphabetically')}`
                : ''}
            </DialogDescription>
          </DialogHeader>
          {accessEntry?.ok ? (
            <>
              <Input
                value={modelFilter}
                placeholder={t('Filter models…') as string}
                className='h-7 text-xs'
                onChange={(e) => setModelFilter(e.target.value)}
              />
              <div className='flex max-h-[55vh] flex-wrap gap-1.5 overflow-y-auto rounded-md border p-2'>
                {accessModels.map((m) => {
                  const p = keyPrice(m)
                  return (
                    <span
                      key={m}
                      title={p?.title}
                      className='inline-flex items-center gap-1 rounded-md border bg-muted/30 px-2 py-0.5 font-mono text-xs'
                    >
                      {m}
                      {p && <span className='text-muted-foreground'>{p.text}</span>}
                    </span>
                  )
                })}
                {accessModels.length === 0 && (
                  <span className='text-muted-foreground text-xs'>
                    {t('No models match the filter')}
                  </span>
                )}
              </div>
            </>
          ) : (
            <div className='text-destructive text-xs'>
              {accessEntry ? `${accessEntry.error || ''} ${accessEntry.detail || ''}`.trim() : t('Request failed')}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </Card>
  )
}
