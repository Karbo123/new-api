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
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ArrowDown, ArrowUp, Filter, PencilLine, Pin, PlayCircle, Plus, Trash2, TrendingUp, X } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { PC_SOURCES, pcCatLabel, pcDiscoverDims, pcSourceIsDynamic, type PriceCompareState } from '../use-price-compare'
import { StepperInput } from './stepper-input'
import type { PcAutoRule, PcRuleTerm, PcRuleTermType } from '../types'

const DEFAULT_RULE: PcAutoRule = {
  top_n: 3,
  sync_channels: false,
  route_channels: false,
  terms: [],
}

// 三种规则方式：弹窗里以分段容器切换（样式对齐 /profile 设置卡）
const MODE_CHIPS: { type: PcRuleTermType; labelKey: string; icon: ReactNode }[] = [
  { type: 'pareto', labelKey: 'Pareto frontier', icon: <TrendingUp className='h-4 w-4' /> },
  { type: 'fixed', labelKey: 'Fixed models', icon: <Pin className='h-4 w-4' /> },
  { type: 'filter', labelKey: 'Filters', icon: <Filter className='h-4 w-4' /> },
]

// 列表行 Badge 用的类型→文案，直接由 MODE_CHIPS 派生（避免两处重复维护）
const MODE_LABEL = Object.fromEntries(
  MODE_CHIPS.map((m) => [m.type, m.labelKey] as const)
) as Record<PcRuleTermType, string>

// 支持自动同步配置的本地 harness（与后端 pcHarnessList 一致）
const PC_HARNESSES: { key: string; label: string }[] = [
  { key: 'claude', label: 'Claude Code' },
  { key: 'codex', label: 'Codex' },
  { key: 'opencode', label: 'OpenCode' },
  { key: 'zcode', label: 'ZCode' },
]

// 全部可选 benchmark 维度（静态注册表 + 动态来源从数据发现），按「榜单源 · 分类」
// 分组展示——子榜单几十项时用户能按分类快速定位
function useAllDims(
  pc: PriceCompareState
): { group: string; items: { key: string; label: string }[] }[] {
  const { t } = useTranslation()
  return useMemo(() => {
    const meta = pc.unified.data?.bench_meta ?? {}
    const map = new Map<string, { key: string; label: string }[]>()
    PC_SOURCES.forEach((s) => {
      const dims = pcSourceIsDynamic(s.key)
        ? pcDiscoverDims(s.key, pc.rows, meta)
        : s.dims
      dims.forEach((d) => {
        const group = `${t(s.label)} · ${t(pcCatLabel(d.cat || 'other'))}`
        if (!map.has(group)) map.set(group, [])
        map.get(group)!.push({ key: d.key, label: `${t(s.label)} · ${t(d.label)}` })
      })
    })
    return [...map.entries()].map(([group, items]) => ({ group, items }))
  }, [pc.rows, pc.unified.data, t])
}

export function AutoRuleCard({ pc }: { pc: PriceCompareState }) {
  const { t } = useTranslation()
  const [rule, setRule] = useState<PcAutoRule>(DEFAULT_RULE)
  const [loaded, setLoaded] = useState(false)
  const [applying, setApplying] = useState(false)
  const dims = useAllDims(pc)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const ar = pc.priority?.auto_rule
    if (ar && !loaded) {
      const merged: PcAutoRule = { ...DEFAULT_RULE, ...ar }
      // 旧单模式配置迁移为规则项（帕累托多维度 / 固定模型 / 筛选器），
      // 旧全局 top_n 落到每条迁移出的规则项上
      if (!merged.terms?.length) {
        const terms: PcRuleTerm[] = []
        const legacyN = merged.top_n ?? 3
        if (merged.pareto?.length) {
          for (const t0 of merged.pareto) {
            terms.push({
              type: 'pareto',
              dim: t0.dim,
              max_actual: t0.max_actual,
              min_score: t0.min_score,
              top_models: t0.top_models,
              top_n: legacyN,
            })
          }
        } else if (merged.pareto_dim) {
          terms.push({ type: 'pareto', dim: merged.pareto_dim, top_n: legacyN })
        }
        if (merged.fixed_models?.length) {
          terms.push({ type: 'fixed', models: merged.fixed_models, top_n: legacyN })
        }
        if (merged.mode === 'filter') {
          terms.push({
            type: 'filter',
            score_dim: merged.score_dim || 'livebench:average',
            max_actual: merged.max_actual ?? 0,
            min_score: merged.min_score ?? 0,
            max_models: merged.max_models ?? 0,
            top_n: legacyN,
          })
        }
        merged.terms = terms
      } else {
        // per-term 改造前保存的旧 terms 没有 top_n 字段（后端语义=不限），
        // 载入时补上旧全局 N，避免显示 N3 实际不限
        const legacyN = merged.top_n ?? 3
        merged.terms = merged.terms.map((t0) => ({ ...t0, top_n: t0.top_n ?? legacyN }))
      }
      setRule(merged)
      setLoaded(true)
    }
  }, [pc.priority, loaded])

  // 本地状态即时生效，保存防抖 600ms——逐键输入时避免并发请求乱序回滚
  const update = (patch: Partial<PcAutoRule>) => {
    const next = { ...rule, ...patch }
    setRule(next)
    if (saveTimer.current) clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => {
      saveTimer.current = null
      pc.saveAutoRule(next).catch(() => toast.error(t('Failed to save settings')))
    }, 600)
  }

  const applyNow = async () => {
    try {
      setApplying(true)
      // 先落盘待保存的规则（防抖窗口内的修改），再按服务端最新配置应用
      if (saveTimer.current) {
        clearTimeout(saveTimer.current)
        saveTimer.current = null
        await pc.saveAutoRule(rule)
      }
      const res = await pc.applyAutoRule()
      if (!((rule.terms?.length ?? 0) > 0)) {
        toast.info(t('Auto update rule is off'))
      } else {
        toast.success(
          t('Applied: {{models}} models · {{offers}} offers', {
            models: res.models,
            offers: res.offers,
          }) +
            (res.channels != null && res.channels > 0
              ? ` · ${t('{{count}} channels updated', { count: res.channels })}`
              : '') +
            (res.routes != null && res.routes > 0
              ? ` · ${t('{{count}} models routed by price', { count: res.routes })}`
              : '') +
            (res.missing.length
              ? ` · ${t('{{count}} fixed models not matched', { count: res.missing.length })}: ${res.missing.join(', ')}`
              : '') +
            (res.conn_dropped != null && res.conn_dropped > 0
              ? ` · ${t('{{count}} unreachable offers dropped', { count: res.conn_dropped })}`
              : '') +
            ((res.harness?.length ?? 0) > 0
              ? ` · ${res.harness!.join(' · ')}`
              : '')
        )
      }
    } catch {
      toast.error(t('Failed to apply auto update rule'))
    } finally {
      setApplying(false)
    }
  }

  const dimSelect = (value: string, onChange: (v: string) => void) => (
    <Select value={value} onValueChange={(v) => v && onChange(v)}>
      <SelectTrigger className='w-full min-w-[220px]'>
        <SelectValue>
          {dims.flatMap((g) => g.items).find((d) => d.key === value)?.label ??
            value}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {dims.map((g) => (
          <SelectGroup key={g.group}>
            <SelectLabel className='text-muted-foreground text-xs'>
              {g.group}
            </SelectLabel>
            {g.items.map((d) => (
              <SelectItem key={d.key} value={d.key}>
                {d.label}
              </SelectItem>
            ))}
          </SelectGroup>
        ))}
      </SelectContent>
    </Select>
  )

  // ---- 规则项：列表 + 弹窗编辑（弹窗内以 chip 切换三种方式）----
  const [termDraft, setTermDraft] = useState<PcRuleTerm | null>(null)
  const [termIdx, setTermIdx] = useState(-1) // -1 = 新增
  const [fixQuery, setFixQuery] = useState('')
  const fixCandidates = useMemo(() => {
    const q = fixQuery.trim().toLowerCase()
    const sel = new Set(termDraft?.models ?? [])
    return Array.from(new Set(pc.rows.map((r) => r.name)))
      .filter((n) => n.toLowerCase().includes(q) && !sel.has(n))
      .sort()
      .slice(0, 30)
  }, [fixQuery, pc.rows, termDraft])
  const openTermDialog = (idx: number) => {
    const cur = rule.terms ?? []
    setTermIdx(idx)
    setFixQuery('')
    if (cur[idx]) {
      setTermDraft({ ...cur[idx] })
    } else {
      setTermDraft({
        type: 'pareto',
        dim: dims[0]?.items[0]?.key ?? 'livebench:average',
        // 后端默认（全新/旧库配置）即返回 top_n=3，已保存配置返回显式保存值
        // （0=不限亦原样返回，零值无 omitempty）；0 仍视为未设置，
        // 新规则项按默认 fallback N=3 创建，仅显式保存过正数才沿用
        top_n: rule.top_n > 0 ? rule.top_n : 3,
      })
    }
  }
  const saveTermDraft = () => {
    if (!termDraft) return
    const t0 = termDraft
    const clean: PcRuleTerm = { type: t0.type, top_n: t0.top_n ?? 3 }
    if (t0.type === 'pareto') {
      clean.dim = t0.dim || dims[0]?.items[0]?.key || 'livebench:average'
      clean.max_actual = t0.max_actual ?? 0
      clean.min_score = t0.min_score ?? 0
      clean.top_models = t0.top_models ?? 0
    } else if (t0.type === 'fixed') {
      clean.models = t0.models ?? []
    } else {
      clean.score_dim = t0.score_dim || dims[0]?.items[0]?.key || 'livebench:average'
      clean.max_actual = t0.max_actual ?? 0
      clean.min_score = t0.min_score ?? 0
      clean.include_unscored = t0.include_unscored ?? false
      clean.max_models = t0.max_models ?? 0
    }
    const list = [...(rule.terms ?? [])]
    if (termIdx >= 0 && termIdx < list.length) list[termIdx] = clean
    else list.push(clean)
    update({ terms: list })
    setTermDraft(null)
  }
  const removeTerm = (idx: number) => {
    const list = [...(rule.terms ?? [])]
    list.splice(idx, 1)
    update({ terms: list })
  }
  // 上移/下移规则项（并集语义下顺序不影响结果集，但保存并保持列表展示顺序）
  const moveTerm = (idx: number, dir: -1 | 1) => {
    const list = [...(rule.terms ?? [])]
    const j = idx + dir
    if (j < 0 || j >= list.length) return
    ;[list[idx], list[j]] = [list[j], list[idx]]
    update({ terms: list })
  }
  const dimLabelOf = (key?: string) =>
    (key ? (dims.flatMap((g) => g.items).find((d) => d.key === key)?.label ?? '') : '') || key || ''
  const termSummary = (tm: PcRuleTerm) => {
    const parts: string[] = []
    if (tm.type === 'pareto') {
      parts.push(dimLabelOf(tm.dim) || tm.dim || '')
      if (tm.max_actual) parts.push(`≤¥${tm.max_actual}/M`)
      if (tm.min_score) parts.push(`≥${tm.min_score}`)
      if (tm.top_models) parts.push(`Top${tm.top_models}`)
    } else if (tm.type === 'fixed') {
      parts.push((tm.models ?? []).join(', '))
    } else {
      parts.push(dimLabelOf(tm.score_dim) || tm.score_dim || '')
      if (tm.max_actual) parts.push(`≤¥${tm.max_actual}/M`)
      if (tm.min_score) parts.push(`≥${tm.min_score}${tm.include_unscored ? '（无分也收）' : ''}`)
      if (tm.max_models) parts.push(`≤${tm.max_models}`)
    }
    // 部件为空（如 fixed 项未选模型）时返回空串，由列表行的「无额外筛选」兜底
    const head = parts.filter(Boolean).join(' · ')
    return head ? `${head} · N${tm.top_n ?? 3}` : ''
  }

  return (
    <Card>
      <CardHeader>
        <div className='flex items-start justify-between gap-2'>
          <div>
            <CardTitle>{t('Auto update rule')}</CardTitle>
            <CardDescription>
              {t(
                'Union of rule terms — each term picks models its own way (Pareto frontier / fixed models / filters); per model keep top N offers as fallback'
              )}
            </CardDescription>
          </div>
          <Button
            size='sm'
            variant='outline'
            className='flex-none'
            disabled={applying}
            onClick={() => void applyNow()}
          >
            <PlayCircle className='mr-1 h-3.5 w-3.5' />
            {applying ? t('Applying…') : t('Apply now')}
          </Button>
        </div>
      </CardHeader>
      <CardContent className='space-y-3'>
        <div className='flex flex-wrap items-center gap-3'>
          <label className='text-muted-foreground flex cursor-pointer items-center gap-1.5 text-xs'>
            <Checkbox
              checked={rule.sync_channels === true}
              onCheckedChange={(v) => update({ sync_channels: v === true })}
            />
            {t('Apply to new-api channels')}
          </label>
          <label
            className='text-muted-foreground flex cursor-pointer items-center gap-1.5 text-xs'
            title={t(
              'Deterministically prefer the cheapest channel per model; on failure fall through the price order instead of random drift, keeping upstream prompt cache warm'
            )}
          >
            <Checkbox
              checked={rule.route_channels === true}
              onCheckedChange={(v) => update({ route_channels: v === true })}
            />
            {t('Route by price order')}
          </label>
        </div>

        {/* 本地 harness 配置自动同步：应用时把 127.0.0.1 网关地址 + 托管令牌 +
            模型列表写进对应软件的配置文件（首改前自动备份 *.pc-bak） */}
        <div
          className='flex flex-wrap items-center gap-3'
          title={t(
            'On apply, write the local New API URL, a managed token and the model list into these apps so their models follow the priority list automatically'
          )}
        >
          <span className='text-muted-foreground text-xs'>
            {t('Auto-sync local harness configs')}
          </span>
          {PC_HARNESSES.map((h) => (
            <label
              key={h.key}
              className='text-muted-foreground flex cursor-pointer items-center gap-1.5 text-xs'
            >
              <Checkbox
                checked={(rule.harnesses ?? []).includes(h.key)}
                onCheckedChange={(v) => {
                  const cur = rule.harnesses ?? []
                  update({
                    harnesses: v === true ? [...cur, h.key] : cur.filter((x) => x !== h.key),
                  })
                }}
              />
              {h.label}
            </label>
          ))}
        </div>

        {/* 规则项列表：三种方式取并集；fallback N 在每条规则项的编辑弹窗里独立设置 */}
        <div className='space-y-2'>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <span className='text-muted-foreground text-xs'>
              {t('Rule terms (union)')}
            </span>
            <Button
              size='sm'
              variant='outline'
              className='h-7'
              onClick={() => openTermDialog(-1)}
            >
              <Plus className='mr-1 h-3 w-3' />
              {t('Add rule')}
            </Button>
          </div>
          <div className='rounded-md border'>
            {(rule.terms ?? []).length === 0 && (
              <div className='text-muted-foreground p-2 text-center text-xs'>
                {t(
                  'No rule terms yet — add one; models from all terms are merged (union)'
                )}
              </div>
            )}
            {(rule.terms ?? []).map((tm, i) => (
              <div
                key={`${tm.type}-${i}`}
                className='flex items-center gap-2 border-b px-2 py-1.5 text-xs last:border-b-0'
              >
                <Badge variant='secondary' className='flex-none'>
                  {t(MODE_LABEL[tm.type] ?? tm.type)}
                </Badge>
                <span className='text-muted-foreground flex-1 truncate'>
                  {termSummary(tm) || t('No extra filters')}
                </span>
                <Button
                  size='sm'
                  variant='ghost'
                  className='h-6 px-1'
                  disabled={i === 0}
                  aria-label={t('Move up')}
                  onClick={() => moveTerm(i, -1)}
                >
                  <ArrowUp className='h-3 w-3' />
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  className='h-6 px-1'
                  disabled={i === (rule.terms?.length ?? 0) - 1}
                  aria-label={t('Move down')}
                  onClick={() => moveTerm(i, 1)}
                >
                  <ArrowDown className='h-3 w-3' />
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  className='h-6 px-1'
                  onClick={() => openTermDialog(i)}
                >
                  <PencilLine className='h-3 w-3' />
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  className='text-destructive h-6 px-1'
                  onClick={() => removeTerm(i)}
                >
                  <Trash2 className='h-3 w-3' />
                </Button>
              </div>
            ))}
          </div>
        </div>
      </CardContent>

      {/* 添加/编辑规则项弹窗：chip 切换三种方式 */}
      <Dialog
        open={termDraft != null}
        onOpenChange={(v) => {
          if (!v) setTermDraft(null)
        }}
      >
        <DialogContent className='sm:max-w-md'>
          <DialogHeader>
            <DialogTitle>
              {termIdx >= 0 ? t('Edit rule') : t('Add rule')}
            </DialogTitle>
            <DialogDescription>
              {t(
                'Pick a rule type; models picked by every term are merged (union) into the auto update'
              )}
            </DialogDescription>
          </DialogHeader>
          {termDraft && (
            <div className='space-y-3'>
              {/* 三种方式在同一个分段容器内切换（样式对齐 /profile 设置卡） */}
              <Tabs
                value={termDraft.type}
                onValueChange={(v) =>
                  v && setTermDraft({ ...termDraft, type: v as PcRuleTermType })
                }
              >
                <TabsList className='grid w-full grid-cols-3 items-stretch gap-1 rounded-xl border border-border p-1 group-data-horizontal/tabs:h-10'>
                  {MODE_CHIPS.map((m) => (
                    <TabsTrigger
                      key={m.type}
                      value={m.type}
                      className='h-full gap-2 rounded-lg px-3 py-0 leading-none'
                    >
                      {m.icon}
                      {t(m.labelKey)}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
              {/* 本项 fallback N（三种类型通用）：本项选中的模型各保留 N 条报价 */}
              <div className='flex items-center gap-2 text-xs'>
                <span className='text-muted-foreground w-28 flex-none'>
                  {t('Offers kept per model (fallback N)')}
                </span>
                <StepperInput
                  value={termDraft.top_n ?? 3}
                  onChange={(v) => setTermDraft({ ...termDraft, top_n: v })}
                  min={0}
                  step={1}
                />
                <span className='text-muted-foreground'>
                  {t('0 means no limit')}
                </span>
              </div>
              {termDraft.type === 'pareto' && (
                <div className='space-y-3'>
                  <div className='space-y-2'>
                    <span className='text-muted-foreground text-xs'>
                      {t('Benchmark dimension')}
                    </span>
                    {dimSelect(termDraft.dim ?? '', (v) =>
                      setTermDraft({ ...termDraft, dim: v })
                    )}
                  </div>
                  <div className='flex items-center gap-2 text-xs'>
                    <span className='text-muted-foreground w-28 flex-none'>
                      {t('Max actual price ¥/M')}
                    </span>
                    <StepperInput
                      value={termDraft.max_actual ?? 0}
                      onChange={(v) =>
                        setTermDraft({ ...termDraft, max_actual: v })
                      }
                      min={0}
                      step={0.01}
                    />
                    <span className='text-muted-foreground'>
                      {t('0 means no limit')}
                    </span>
                  </div>
                  <div className='flex items-center gap-2 text-xs'>
                    <span className='text-muted-foreground w-28 flex-none'>
                      {t('Min score')}
                    </span>
                    <StepperInput
                      value={termDraft.min_score ?? 0}
                      onChange={(v) =>
                        setTermDraft({ ...termDraft, min_score: v })
                      }
                      min={0}
                      step={1}
                    />
                    <span className='text-muted-foreground'>
                      {t('0 means no limit')}
                    </span>
                  </div>
                  <div className='flex items-center gap-2 text-xs'>
                    <span className='text-muted-foreground w-28 flex-none'>
                      {t('Top models per term')}
                    </span>
                    <StepperInput
                      value={termDraft.top_models ?? 0}
                      onChange={(v) =>
                        setTermDraft({ ...termDraft, top_models: v })
                      }
                      min={0}
                      step={1}
                    />
                    <span className='text-muted-foreground'>
                      {t('0 means no limit')}
                    </span>
                  </div>
                </div>
              )}
              {termDraft.type === 'fixed' && (
                <div className='space-y-2'>
                  <Input
                    value={fixQuery}
                    placeholder={(t('Search models') ?? '') as string}
                    className='font-mono text-xs'
                    onChange={(e) => setFixQuery(e.target.value)}
                  />
                  <div className='max-h-40 overflow-auto rounded-md border p-1'>
                    {fixCandidates.map((name) => (
                      <button
                        key={name}
                        type='button'
                        className='hover:bg-muted block w-full cursor-pointer px-2 py-1 text-left font-mono text-xs'
                        onClick={() => {
                          setTermDraft({
                            ...termDraft,
                            models: [...(termDraft.models ?? []), name],
                          })
                          setFixQuery('')
                        }}
                      >
                        {name}
                      </button>
                    ))}
                    {fixCandidates.length === 0 && (
                      <div className='text-muted-foreground px-2 py-1 text-xs'>
                        {t('No match')}
                      </div>
                    )}
                  </div>
                  {(termDraft.models ?? []).length > 0 && (
                    <div className='flex flex-wrap gap-1.5'>
                      {(termDraft.models ?? []).map((m) => (
                        <span
                          key={m}
                          className='inline-flex items-center gap-1 rounded-md border bg-muted/30 py-0.5 pl-2 pr-1 font-mono text-xs'
                        >
                          {m}
                          <button
                            type='button'
                            tabIndex={-1}
                            className='text-muted-foreground hover:text-destructive'
                            onClick={() =>
                              setTermDraft({
                                ...termDraft,
                                models: (termDraft.models ?? []).filter(
                                  (x) => x !== m
                                ),
                              })
                            }
                          >
                            <X className='h-3 w-3' />
                          </button>
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              )}
              {termDraft.type === 'filter' && (
                <div className='space-y-3'>
                  <div className='space-y-2'>
                    <span className='text-muted-foreground text-xs'>
                      {t('Benchmark dimension')}
                    </span>
                    {dimSelect(termDraft.score_dim ?? '', (v) =>
                      setTermDraft({ ...termDraft, score_dim: v })
                    )}
                  </div>
                  <div className='flex items-center gap-2 text-xs'>
                    <span className='text-muted-foreground w-28 flex-none'>
                      {t('Max actual price ¥/M')}
                    </span>
                    <StepperInput
                      value={termDraft.max_actual ?? 0}
                      onChange={(v) =>
                        setTermDraft({ ...termDraft, max_actual: v })
                      }
                      min={0}
                      step={0.01}
                    />
                    <span className='text-muted-foreground'>
                      {t('0 means no limit')}
                    </span>
                  </div>
                  <div className='flex items-center gap-2 text-xs'>
                    <span className='text-muted-foreground w-28 flex-none'>
                      {t('Min score')}
                    </span>
                    <StepperInput
                      value={termDraft.min_score ?? 0}
                      onChange={(v) =>
                        setTermDraft({ ...termDraft, min_score: v })
                      }
                      min={0}
                      step={1}
                    />
                    <span className='text-muted-foreground'>
                      {t('0 means no limit')}
                    </span>
                  </div>
                  <label className='flex items-center gap-2 text-xs'>
                    <Checkbox
                      checked={termDraft.include_unscored ?? false}
                      onCheckedChange={(v) =>
                        setTermDraft({ ...termDraft, include_unscored: v === true })
                      }
                    />
                    <span>{t('Include models without benchmark scores')}</span>
                  </label>
                  <div className='flex items-center gap-2 text-xs'>
                    <span className='text-muted-foreground w-28 flex-none'>
                      {t('Max models')}
                    </span>
                    <StepperInput
                      value={termDraft.max_models ?? 0}
                      onChange={(v) =>
                        setTermDraft({ ...termDraft, max_models: v })
                      }
                      min={0}
                      step={1}
                    />
                    <span className='text-muted-foreground'>
                      {t('0 means no limit')}
                    </span>
                  </div>
                </div>
              )}
              <div className='flex justify-end gap-2'>
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() => setTermDraft(null)}
                >
                  {t('Cancel')}
                </Button>
                <Button size='sm' onClick={saveTermDraft}>
                  {t('Save')}
                </Button>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </Card>
  )
}
