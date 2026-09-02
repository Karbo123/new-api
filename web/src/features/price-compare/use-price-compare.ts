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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'

import { useTranslation } from 'react-i18next'

import {
  applyPcAutoRule,
  getPcOverview,
  getPcPriority,
  getPcRefreshStatus,
  getPcUnified,
  pcQueryKeys,
  postPcRefresh,
  savePcPriority,
} from './api'
import type {
  PcAutoRule,
  PcOverview,
  PcPriorityConfig,
  PcRefreshStatus,
  PcRatio,
  PcUnifiedResponse,
  PcUnifiedRow,
} from './types'

// 榜单源注册表：各榜单量纲不同（LiveBench/LLM Stats/司南 0-100，Arena 净提升%，AA 指数，
// Elo，用量份额），二选一查看，绝不混用。维度键格式 "来源:字段"，后端 row.bench[来源].scores[字段]。
// label 为 i18n 键（英文），渲染处统一 t()。
export interface PcDim {
  key: string
  label: string
  rank?: boolean
  // 所属分类（编程/数学/推理…）：三级导航的中间层，动态维度由 pcClassifyDim 兜底
  cat?: string
}
export interface PcSourceDef {
  key: string
  label: string
  url: string
  dims: PcDim[]
  // 动态来源的主维度（如 AI IQ 的综合 IQ）：发现后排最前并作为切换来源时的默认
  primaryDim?: string
}

// 维度分类注册表：顺序即分类 tab 的展示顺序（主维度的分类恒最前）
export const PC_CATS: { key: string; label: string }[] = [
  { key: 'overall', label: 'Overall' },
  { key: 'coding', label: 'Coding' },
  { key: 'math', label: 'Math' },
  { key: 'reasoning', label: 'Reasoning' },
  { key: 'agentic', label: 'Agentic' },
  { key: 'instruction', label: 'Instruction Following' },
  { key: 'knowledge', label: 'Knowledge' },
  { key: 'text', label: 'Text' },
  { key: 'vision', label: 'Vision' },
  { key: 'data', label: 'Data Analysis' },
  { key: 'longctx', label: 'Long Context' },
  { key: 'domain', label: 'Domain expertise' },
  { key: 'safety', label: 'Safety' },
  { key: 'iq', label: 'IQ' },
  { key: 'search', label: 'Search' },
  { key: 'other', label: 'Other' },
]
export function pcCatLabel(cat: string): string {
  return PC_CATS.find((c) => c.key === cat)?.label ?? cat
}

// 动态来源（LLM Stats / AI IQ）的维度自动分类：按官方展示名+字段键的关键词归入
// 分类注册表；规则顺序即优先级（如 "xxx IQ" 必须先于 reasoning，防 "Abstract
// Reasoning IQ" 被抢走）
export function pcClassifyDim(key: string, label: string): string {
  const s = `${label} ${key}`.toLowerCase()
  const has = (re: RegExp) => re.test(s)
  if (has(/\biq\b|\beq\b|emotional/)) return 'iq'
  if (has(/code|swe|program|livecodebench|ioi|scicode/)) return 'coding'
  if (has(/math|aime|frontiermath|proof|critpt|algebra/)) return 'math'
  if (has(/ifbench|ifeval|instruct|instruction/)) return 'instruction'
  if (
    has(
      /agent|tool|osworld|mcp|browse|tau|ops|apex|computer use|reliability|terminal|bash|briefcase/
    )
  )
    return 'agentic'
  if (has(/long[ -]?context/)) return 'longctx'
  if (has(/gpqa|hle|reason|arc[ -]?agi|abstract/)) return 'reasoning'
  if (has(/vision|image|mmmu|multimodal|video/)) return 'vision'
  if (has(/mmlu|knowledge|omniscience|simpleqa|facts|ground/)) return 'knowledge'
  if (has(/text|writ|document|literature|creative/)) return 'text'
  if (has(/safety|fairness|morality|legal|privacy|align/)) return 'safety'
  if (has(/data[ -]?analysis/)) return 'data'
  if (has(/search/)) return 'search'
  if (has(/financ|health|econ|strateg|gdpval/)) return 'domain'
  return 'other'
}
export const PC_SOURCES: PcSourceDef[] = [
  {
    key: 'livebench',
    label: 'LiveBench',
    url: 'https://livebench.ai',
    dims: [
      { key: 'livebench:average', label: 'Overall', cat: 'overall' },
      { key: 'livebench:reasoning', label: 'Reasoning', cat: 'reasoning' },
      { key: 'livebench:coding', label: 'Coding', cat: 'coding' },
      { key: 'livebench:agentic_coding', label: 'Agentic Coding', cat: 'agentic' },
      { key: 'livebench:math', label: 'Math', cat: 'math' },
      { key: 'livebench:data_analysis', label: 'Data Analysis', cat: 'data' },
      { key: 'livebench:language', label: 'Language', cat: 'text' },
      { key: 'livebench:instruction_following', label: 'Instruction Following', cat: 'instruction' },
    ],
  },
  {
    key: 'arena',
    label: 'Agent Arena',
    url: 'https://arena.ai/leaderboard',
    // 排序约定：总排名(综合)组在前、组内「排名」维度置顶——用户最先找的是
    // 编程/综合等方向的真实排名与总分，细分信号（净提升/确认成功…）跟在后面
    dims: [
      { key: 'arena:overall_rank', label: 'Overall · Rank', rank: true, cat: 'overall' },
      { key: 'arena:overall_net', label: 'Overall · Net Improvement', cat: 'overall' },
      { key: 'arena:overall_success', label: 'Overall · Confirmed Success', cat: 'overall' },
      { key: 'arena:overall_praise', label: 'Overall · Praise Ratio', cat: 'overall' },
      { key: 'arena:overall_steer', label: 'Overall · Steerability', cat: 'overall' },
      { key: 'arena:overall_bash', label: 'Overall · Bash Recovery', cat: 'overall' },
      { key: 'arena:overall_halluc', label: 'Overall · Tool Hallucination', cat: 'overall' },
      { key: 'arena:code_rank', label: 'Code · Rank', rank: true, cat: 'coding' },
      { key: 'arena:code_net', label: 'Code · Net Improvement', cat: 'coding' },
      { key: 'arena:code_success', label: 'Code · Confirmed Success', cat: 'coding' },
      { key: 'arena:code_praise', label: 'Code · Praise Ratio', cat: 'coding' },
      { key: 'arena:code_steer', label: 'Code · Steerability', cat: 'coding' },
      { key: 'arena:code_bash', label: 'Code · Bash Recovery', cat: 'coding' },
      { key: 'arena:code_halluc', label: 'Code · Tool Hallucination', cat: 'coding' },
      { key: 'arena:text_elo', label: 'Text · Arena Score', cat: 'text' },
      { key: 'arena:text_rank', label: 'Text · Rank', rank: true, cat: 'text' },
      { key: 'arena:webdev_elo', label: 'WebDev · Arena Score', cat: 'coding' },
      { key: 'arena:webdev_rank', label: 'WebDev · Rank', rank: true, cat: 'coding' },
      { key: 'arena:i2w_elo', label: 'Image→WebDev · Arena Score', cat: 'coding' },
      { key: 'arena:i2w_rank', label: 'Image→WebDev · Rank', rank: true, cat: 'coding' },
      { key: 'arena:vision_elo', label: 'Vision · Arena Score', cat: 'vision' },
      { key: 'arena:vision_rank', label: 'Vision · Rank', rank: true, cat: 'vision' },
      { key: 'arena:search_elo', label: 'Search · Arena Score', cat: 'search' },
      { key: 'arena:search_rank', label: 'Search · Rank', rank: true, cat: 'search' },
      { key: 'arena:t2i_elo', label: 'T2I · Arena Score', cat: 'vision' },
      { key: 'arena:t2i_rank', label: 'T2I · Rank', rank: true, cat: 'vision' },
      { key: 'arena:document_elo', label: 'Document · Arena Score', cat: 'text' },
      { key: 'arena:document_rank', label: 'Document · Rank', rank: true, cat: 'text' },
    ],
  },
  {
    key: 'ls',
    label: 'LLM Stats',
    url: 'https://llm-stats.com',
    dims: [], // 动态维度：pcDiscoverDims 从数据发现
  },
  {
    key: 'aiq',
    label: 'AI IQ',
    url: 'https://www.aiiq.org/',
    dims: [], // 动态维度：综合 IQ/六维 IQ/EQ/底层基准，从数据发现（label 用后端 bench_meta 官方名）
    primaryDim: 'iq',
  },
  {
    key: 'oc',
    label: 'OpenCompass',
    url: 'https://rank.opencompass.org.cn/leaderboard/llm',
    dims: [
      { key: 'oc:average', label: 'Overall', cat: 'overall' },
      { key: 'oc:knowledge', label: 'Knowledge', cat: 'knowledge' },
      { key: 'oc:reasoning', label: 'Reasoning', cat: 'reasoning' },
      { key: 'oc:math', label: 'Math', cat: 'math' },
      { key: 'oc:coding', label: 'Coding', cat: 'coding' },
      { key: 'oc:open_avg', label: 'Open-source Avg', cat: 'overall' },
      { key: 'oc:open_hle', label: 'HLE', cat: 'reasoning' },
      { key: 'oc:open_aime25', label: 'AIME 2025', cat: 'math' },
      { key: 'oc:open_mmlupro', label: 'MMLU-Pro', cat: 'knowledge' },
      { key: 'oc:open_lcb6', label: 'LiveCodeBench v6', cat: 'coding' },
      { key: 'oc:open_gpqa', label: 'GPQA Diamond', cat: 'reasoning' },
      { key: 'oc:open_ifeval', label: 'IFEval', cat: 'instruction' },
      { key: 'oc:sec_score', label: 'Value Alignment Score', cat: 'safety' },
      { key: 'oc:sec_fairness', label: 'Fairness', cat: 'safety' },
      { key: 'oc:sec_safety', label: 'Safety', cat: 'safety' },
      { key: 'oc:sec_morality', label: 'Morality', cat: 'safety' },
      { key: 'oc:sec_legality', label: 'Legality', cat: 'safety' },
      { key: 'oc:sec_privacy', label: 'Data Protection', cat: 'safety' },
      { key: 'oc:arena_elo', label: 'Compass Arena · Elo', cat: 'text' },
      { key: 'oc:arena_rank', label: 'Compass Arena · Rank', rank: true, cat: 'text' },
    ],
  },
  {
    key: 'da',
    label: 'Design Arena',
    url: 'https://designarena.ai/leaderboard',
    dims: [
      { key: 'da:elo', label: 'Elo', cat: 'overall' },
      { key: 'da:win', label: 'Win rate', cat: 'overall' },
      { key: 'da:fs_elo', label: 'FullStack · Elo', cat: 'coding' },
      { key: 'da:fs_win', label: 'FullStack · Win rate', cat: 'coding' },
    ],
  },
  {
    key: 'aa',
    label: 'Artificial Analysis',
    url: 'https://artificialanalysis.ai/models',
    dims: [
      { key: 'aa:intelligence', label: 'Intelligence Index', cat: 'overall' },
      { key: 'aa:omniscience', label: 'Omniscience', cat: 'knowledge' },
      { key: 'aa:cap_financeAndAccounting', label: 'Finance & Accounting', cat: 'domain' },
      { key: 'aa:cap_strategyAndOps', label: 'Strategy & Ops', cat: 'domain' },
      { key: 'aa:cap_legal', label: 'Legal', cat: 'domain' },
      { key: 'aa:cap_healthcareAndMedical', label: 'Healthcare', cat: 'domain' },
      { key: 'aa:cap_engineering', label: 'Engineering', cat: 'domain' },
      { key: 'aa:cap_economics', label: 'Economics', cat: 'domain' },
      { key: 'aa:tb21', label: 'Terminal-Bench 2.1', cat: 'agentic' },
      { key: 'aa:tb40', label: 'Terminal-Bench 4.0', cat: 'agentic' },
      { key: 'aa:tb_hard', label: 'Terminal-Bench Hard', cat: 'agentic' },
      { key: 'aa:gpqa', label: 'GPQA Diamond', cat: 'reasoning' },
      { key: 'aa:hle', label: "Humanity's Last Exam", cat: 'reasoning' },
      { key: 'aa:ifbench', label: 'IFBench', cat: 'instruction' },
      { key: 'aa:scicode', label: 'SciCode', cat: 'coding' },
      { key: 'aa:critpt', label: 'CritPt', cat: 'math' },
      { key: 'aa:mmmupro', label: 'MMMU-Pro', cat: 'vision' },
      { key: 'aa:gdpval', label: 'GDPval', cat: 'domain' },
      { key: 'aa:mlcr', label: 'MLCR', cat: 'reasoning' },
      { key: 'aa:tau_banking', label: 'Tau2 Banking', cat: 'agentic' },
      { key: 'aa:autobench', label: 'AutomationBench', cat: 'agentic' },
      { key: 'aa:ops_gym', label: 'Enterprise Ops Gym', cat: 'agentic' },
      { key: 'aa:apex_agents', label: 'Apex Agents', cat: 'agentic' },
      { key: 'aa:briefcase_elo', label: 'Briefcase Elo', cat: 'agentic' },
      { key: 'aa:ca_index', label: 'Coding Agents Index', cat: 'coding' },
      { key: 'aa:ca_deepswe', label: 'DeepSWE v1.1 · Agent', cat: 'coding' },
      { key: 'aa:ca_sweatlas', label: 'SWE-Atlas QnA · Agent', cat: 'coding' },
      { key: 'aa:ca_tb4', label: 'Terminal-Bench v4 · Agent', cat: 'agentic' },
    ],
  },
  {
    key: 'orr',
    label: 'OpenRouter Usage',
    url: 'https://openrouter.ai/rankings',
    dims: [
      { key: 'orr:share', label: 'Usage share', cat: 'overall' },
      { key: 'orr:usage_rank', label: 'Usage rank', rank: true, cat: 'overall' },
    ],
  },
]

// 从行数据发现动态来源（dims 为空的来源：LLM Stats / AI IQ）可用维度，
// 按覆盖行数排序，label 用后端 bench_meta 官方名
export function pcDiscoverDims(
  src: string,
  rows: PcUnifiedRow[],
  meta: Record<string, string> = {}
): PcDim[] {
  const counts = new Map<string, number>()
  rows.forEach((r) => {
    const s = r.bench?.[src]?.scores
    if (s) Object.keys(s).forEach((k) => counts.set(k, (counts.get(k) ?? 0) + 1))
  })
  const pretty = (id: string) =>
    id
      .split(/[-_]/)
      .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
      .join(' ')
  const scoreKeys = [...counts.keys()]
    .filter((k) => !k.endsWith('_rank'))
    .sort((a, b) => (counts.get(b) ?? 0) - (counts.get(a) ?? 0) || a.localeCompare(b))
  const dims: PcDim[] = []
  scoreKeys.forEach((k) => {
    const label = meta[k] ?? pretty(k)
    const cat = pcClassifyDim(k, label)
    dims.push({ key: `${src}:${k}`, label, cat })
    if (counts.has(`${k}_rank`))
      dims.push({ key: `${src}:${k}_rank`, label: `${label} · Rank`, rank: true, cat })
  })
  // 主维度（来源的头号指标）排最前，切换来源时 discovered[0] 即默认选中
  const primary = pcSourceDef(src)?.primaryDim
  if (primary) {
    const i = dims.findIndex((d) => d.key === `${src}:${primary}`)
    if (i > 0) {
      const [d] = dims.splice(i, 1)
      dims.unshift(d)
      const ri = dims.findIndex((x) => x.key === `${src}:${primary}_rank`)
      if (ri > 1) {
        const [r] = dims.splice(ri, 1)
        dims.splice(1, 0, r)
      }
    }
  }
  return dims
}

// 动态来源：dims 为空即表示维度从行数据发现（当前 ls / aiq）
export function pcSourceIsDynamic(src: string): boolean {
  return (pcSourceDef(src)?.dims.length ?? 1) === 0
}

// 空数据状态文案：区分「加载失败 / 加载中 / 后台预热中 / 暂无数据」——预热可能
// 超过 3 分钟，不能一直显示"加载中"让用户以为页面坏了；查询失败（retry:false）
// 也不会自动重试，必须明确报错而非停在"加载中"
export function pcEmptyStateText(
  unified: PcUnifiedResponse | undefined,
  t: (k: string) => string,
  error?: unknown
): string {
  if (error) return t('Failed to load')
  if (unified == null) return t('Loading')
  if (unified.rows.length === 0) {
    return unified.refresh?.running
      ? t('Warming up data in the background — this can take a few minutes')
      : t('No data yet — click "Refresh now" to fetch')
  }
  return t('No scored models under current filters')
}

export function pcSourceOf(dim: string): string {
  return dim.split(':')[0] || 'livebench'
}
export function pcSourceDef(src: string): PcSourceDef | undefined {
  return PC_SOURCES.find((d) => d.key === src)
}

// 旧配置值迁移（score_dim 曾存裸键）
const PC_LEGACY_DIM: Record<string, string> = {
  average: 'livebench:average',
  reasoning: 'livebench:reasoning',
  coding: 'livebench:coding',
  data_analysis: 'livebench:data_analysis',
  agentic_coding: 'livebench:agentic_coding',
  math: 'livebench:math',
  language: 'livebench:language',
  instruction_following: 'livebench:instruction_following',
  arena_code: 'arena:code_net',
  arena_overall: 'arena:overall_net',
}
export type PcScoreDim = string

export const PC_WINDOW_LABELS: Record<string, string> = {
  day: 'Last 1 day',
  week: 'Last 1 week',
  month: 'Last 1 month',
  halfyear: 'Last half year',
  year: 'Last 1 year',
  all: 'All history',
  custom: 'Manual input',
}

export const PC_REFRESH_INTERVALS = [
  { value: '10m', label: 'Every 10 minutes' },
  { value: '30m', label: 'Every half hour' },
  { value: '1h', label: 'Every 1 hour' },
  { value: '6h', label: 'Every 6 hours' },
  { value: '12h', label: 'Every 12 hours' },
  { value: '1d', label: 'Every day' },
  { value: '1w', label: 'Every week' },
  { value: '1M', label: 'Every month' },
  { value: 'never', label: 'Never' },
]

export function usePriceCompare() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const overview = useQuery<PcOverview>({
    queryKey: pcQueryKeys.overview(),
    queryFn: getPcOverview,
    staleTime: 60_000,
    retry: false,
  })
  const unified = useQuery<PcUnifiedResponse>({
    queryKey: pcQueryKeys.unified(),
    queryFn: getPcUnified,
    staleTime: 60_000,
    retry: false,
  })
  const priority = useQuery<PcPriorityConfig>({
    queryKey: pcQueryKeys.priority(),
    queryFn: getPcPriority,
    staleTime: 60_000,
    retry: false,
  })

  const [refreshStatus, setRefreshStatus] = useState<PcRefreshStatus | null>(
    null
  )
  const [refreshing, setRefreshing] = useState(false)

  // 配置状态（进入页面时从服务端配置同步一次）
  const [window, setWindow] = useState<string>('month')
  const [custom, setCustom] = useState({
    out_in: 10,
    cache_hit: 90,
    cache_write: 0,
  })
  const [scoreDim, setScoreDim] = useState<PcScoreDim>('livebench:average')
  // 渠道开关：语义与后端 chOk 一致——键缺失 = 开启（中转渠道 buzzai/apikl/
  // apib/ikun 默认不在 map 里即默认展示，过滤处统一用 `!== false` 判断）
  const [channels, setChannels] = useState<Record<string, boolean>>({
    openrouter: true,
    opencode: true,
    deepseek: true,
  })
  const [refreshInterval, setRefreshInterval] = useState<string>('12h')
  const cfgLoadedRef = useRef(false)
  useEffect(() => {
    if (priority.data && !cfgLoadedRef.current) {
      cfgLoadedRef.current = true
      setWindow(priority.data.window || 'month')
      setCustom({
        out_in: priority.data.custom?.out_in ?? 10,
        cache_hit: priority.data.custom?.cache_hit ?? 90,
        cache_write: priority.data.custom?.cache_write ?? 0,
      })
      setScoreDim(PC_LEGACY_DIM[priority.data.score_dim as string] || (priority.data.score_dim as PcScoreDim) || 'livebench:average')
      if (priority.data.channels) {
        setChannels({ ...priority.data.channels })
      }
      setRefreshInterval(priority.data.refresh_interval || '12h')
    }
  }, [priority.data])

  // 保存串行化：防抖保存与非防抖写者（优先级增删 / 自动规则）排进同一条链，
  // 每个任务轮到自己时才读 query 缓存快照并 POST——在途保存落地前缓存仍是
  // 旧配置，若此刻并发 POST 会以不含对方修改的快照整体覆盖服务端配置
  const saveChain = useRef<Promise<unknown>>(Promise.resolve())
  const enqueueSave = useCallback((task: () => Promise<void>) => {
    const run = saveChain.current.then(task, task)
    saveChain.current = run.then(
      () => undefined,
      () => undefined
    )
    return run
  }, [])

  // 配置变更防抖保存（触发时读 query 缓存里的最新配置，避免用旧快照覆盖
  // 防抖窗口内其他途径写入的 entries，如优先级增删/自动规则应用）
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const persistConfig = useCallback(
    (patch: Partial<PcPriorityConfig>) => {
      if (saveTimer.current) clearTimeout(saveTimer.current)
      saveTimer.current = setTimeout(() => {
        void enqueueSave(async () => {
          try {
            const current = queryClient.getQueryData<PcPriorityConfig>(
              pcQueryKeys.priority()
            )
            const next: PcPriorityConfig = {
              window: 'month',
              custom: { out_in: 10, cache_hit: 90, cache_write: 0 },
              refresh_interval: '12h',
              score_dim: 'livebench:average',
              channels: {
                openrouter: true,
                opencode: true,
                deepseek: true,
              },
              entries: {},
              ...(current || {}),
              ...patch,
            }
            await savePcPriority(next)
            queryClient.setQueryData(pcQueryKeys.priority(), next)
          } catch {
            toast.error(t('Failed to save settings'))
          }
        })
      }, 600)
    },
    [enqueueSave, queryClient, t]
  )

  const changeWindow = useCallback(
    (w: string) => {
      setWindow(w)
      persistConfig({ window: w })
    },
    [persistConfig]
  )
  const changeCustom = useCallback(
    (patch: Partial<typeof custom>) => {
      setCustom((prev) => {
        const next = { ...prev, ...patch }
        persistConfig({ custom: next })
        return next
      })
    },
    [persistConfig]
  )
  const changeScoreDim = useCallback(
    (d: PcScoreDim) => {
      setScoreDim(d)
      persistConfig({ score_dim: d })
    },
    [persistConfig]
  )
  // 切换榜单来源 = 切到该来源的第一个维度（各榜单分数不混用）；
  // 动态来源（LLM Stats / AI IQ）维度由数据发现，无可发现维度时保持原维度
  const changeScoreSource = useCallback(
    (src: string) => {
      if (pcSourceIsDynamic(src)) {
        const discovered = pcDiscoverDims(
          src,
          unified.data?.rows ?? [],
          unified.data?.bench_meta ?? {}
        )
        const dim = discovered[0]?.key
        if (dim) {
          setScoreDim(dim)
          persistConfig({ score_dim: dim })
        }
        return
      }
      const def = PC_SOURCES.find((d) => d.key === src)
      const dim = def?.dims[0]?.key ?? 'livebench:average'
      setScoreDim(dim)
      persistConfig({ score_dim: dim })
    },
    [persistConfig, unified.data]
  )
  const changeChannels = useCallback(
    (patch: Record<string, boolean>) => {
      setChannels((prev) => {
        const next = { ...prev, ...patch }
        persistConfig({ channels: next })
        return next
      })
    },
    [persistConfig]
  )
  const changeRefreshInterval = useCallback(
    (v: string) => {
      setRefreshInterval(v)
      persistConfig({ refresh_interval: v })
    },
    [persistConfig]
  )

  // 手动刷新 + 轮询进度
  const triggerRefresh = useCallback(async () => {
    try {
      setRefreshing(true)
      await postPcRefresh()
      setRefreshStatus({
        running: true,
        done: 0,
        total: 0,
        current: '',
        finished: false,
      })
    } catch {
      toast.error(t('Failed to trigger refresh'))
      setRefreshing(false)
    }
  }, [t])

  useEffect(() => {
    if (!refreshing) return
    const timer = setInterval(async () => {
      try {
        const st = await getPcRefreshStatus()
        setRefreshStatus(st)
        if (!st.running) {
          setRefreshing(false)
          toast.success(t('Refresh completed'))
          void queryClient.invalidateQueries({ queryKey: pcQueryKeys.all })
        }
      } catch {
        /* ignore */
      }
    }, 2000)
    return () => clearInterval(timer)
  }, [refreshing, queryClient, t])

  // unified 空数据（后端预热/重建中）每 5 秒轮询。后端 refresh 还在跑时无限轮询
  // （有状态背书，完整预热可能超过 3 分钟，36 次上限会让人永远卡在加载中）；
  // 非 running 态才用 36 次上限兜底，防后端异常时无限轮询
  const warmupTries = useRef(0)
  const warmupPending =
    unified.data != null &&
    unified.data.rows.length === 0 &&
    !unified.isFetching &&
    (unified.data.refresh?.running === true || warmupTries.current < 36)
  useEffect(() => {
    if (!warmupPending) return
    const timer = setTimeout(() => {
      warmupTries.current += 1
      void queryClient.invalidateQueries({ queryKey: pcQueryKeys.unified() })
    }, 5000)
    return () => clearTimeout(timer)
  }, [warmupPending, unified.dataUpdatedAt, queryClient])

  const effRatio = useCallback((): PcRatio => {
    if (window === 'custom') {
      const hit = Math.max(0, Math.min(100, custom.cache_hit || 0)) / 100
      const cw = Math.max(0, Math.min(100, custom.cache_write || 0)) / 100
      const ow = Math.max(0, custom.out_in || 0) / 100
      return {
        cache: hit,
        output: ow,
        cache_write: cw,
        uncached: Math.max(0, 1 - hit - cw),
      }
    }
    const r = overview.data?.ratios?.[window]
    return (
      r || { cache: 0.9, output: 0.01, uncached: 0.09, cache_write: 0 }
    )
  }, [window, custom, overview.data])

  const rate = unified.data?.rate ?? overview.data?.exchange.rate ?? 6.7

  const actualOf = useCallback(
    (r: PcUnifiedRow): number => {
      const R = effRatio()
      const cur = r.currency === 'CNY' ? 1 : rate
      return (
        (R.uncached * (r.in || 0) +
          R.cache * (r.read || 0) +
          (R.cache_write || 0) * (r.write || 0) +
          R.output * (r.out || 0)) *
        cur *
        (r.multiplier || 1)
      )
    },
    [effRatio, rate]
  )

  const scoreOf = useCallback(
    (r: PcUnifiedRow): number | null => {
      const [src, field] = scoreDim.split(':')
      const e = r.bench?.[src]
      return e && e.scores && e.scores[field] != null ? e.scores[field] : null
    },
    [scoreDim]
  )

  const rows = useMemo(() => unified.data?.rows ?? [], [unified.data])

  const priorityEntries = useMemo(
    () => priority.data?.entries ?? {},
    [priority.data]
  )

  // ---- 优先级增删改（全部走 savePriority，与防抖保存同链串行） ----
  const mutatePriority = useCallback(
    (mutator: (entries: PcPriorityConfig['entries']) => PcPriorityConfig['entries']) => {
      // 快照在任务轮到执行时才读：排队期间落地的其他保存（含防抖保存）都已
      // 写回缓存，避免互相覆盖
      return enqueueSave(async () => {
        const cached = queryClient.getQueryData<PcPriorityConfig>(
          pcQueryKeys.priority()
        )
        const current: PcPriorityConfig = {
          window,
          custom,
          refresh_interval: refreshInterval,
          score_dim: scoreDim,
          channels,
          ...(cached ?? priority.data ?? {}),
          entries: cached?.entries ?? priority.data?.entries ?? {},
        }
        const next = { ...current, entries: mutator({ ...current.entries }) }
        await savePcPriority(next)
        queryClient.setQueryData(pcQueryKeys.priority(), next)
      })
    },
    [enqueueSave, window, custom, refreshInterval, scoreDim, channels, priority.data, queryClient]
  )

  const addToPriority = useCallback(
    (row: PcUnifiedRow) => {
      const offer = {
        channel: row.channel,
        ref: row.ref,
        name: row.full_name || row.name,
        period: row.period,
        provider: row.provider,
        // 量化档是报价身份的一部分（后端报价键含 quant）：缺失会让 OR 报价
        // 失去量化钉扎，同 provider 不同档位的第二条也会被误判为重复
        quant: row.quant,
      }
      return mutatePriority((entries) => {
        const key = row.key
        const entry = entries[key] || { name: row.name, offers: [] }
        const dup = entry.offers.some(
          (o) =>
            o.channel === offer.channel &&
            o.ref === offer.ref &&
            o.period === offer.period &&
            (o.provider || '') === (offer.provider || '') &&
            (o.quant || '') === (offer.quant || '')
        )
        if (dup) {
          toast.info(t('Already in priority list'))
          return entries
        }
        return {
          ...entries,
          [key]: { name: entry.name || row.name, offers: [...entry.offers, offer] },
        }
      }).then(() => {
        toast.success(t('Added to priority list'))
      }).catch(() => {
        /* 失败已由全局错误提示覆盖 */
      })
    },
    [mutatePriority, t]
  )

  const removeFromPriority = useCallback(
    (key: string, index: number) =>
      mutatePriority((entries) => {
        const entry = entries[key]
        if (!entry) return entries
        const offers = entry.offers.filter((_, i) => i !== index)
        const next = { ...entries }
        if (offers.length === 0) delete next[key]
        else next[key] = { ...entry, offers }
        return next
      }).catch(() => {}),
    [mutatePriority]
  )

  const movePriority = useCallback(
    (key: string, index: number, dir: -1 | 1) =>
      mutatePriority((entries) => {
        const entry = entries[key]
        if (!entry) return entries
        const j = index + dir
        if (j < 0 || j >= entry.offers.length) return entries
        const offers = [...entry.offers]
        ;[offers[index], offers[j]] = [offers[j], offers[index]]
        return { ...entries, [key]: { name: entry.name, offers } }
      }).catch(() => {}),
    [mutatePriority]
  )

  // 自动更新规则：保存配置 / 立即应用（与防抖保存同链串行，快照执行时才读）
  const saveAutoRule = useCallback(
    (rule: PcAutoRule) => {
      return enqueueSave(async () => {
        const cached = queryClient.getQueryData<PcPriorityConfig>(
          pcQueryKeys.priority()
        )
        const current: PcPriorityConfig = {
          window,
          custom,
          refresh_interval: refreshInterval,
          score_dim: scoreDim,
          channels,
          ...(cached ?? priority.data ?? {}),
          entries: cached?.entries ?? priority.data?.entries ?? {},
          auto_rule: rule,
        }
        const saved = await savePcPriority(current)
        queryClient.setQueryData(pcQueryKeys.priority(), saved)
      })
    },
    [enqueueSave, window, custom, refreshInterval, scoreDim, channels, priority.data, queryClient]
  )

  const applyAutoRule = useCallback(async () => {
    // 先等保存链排空（含防抖已触发、仍在途的 saveAutoRule），再按服务端
    // 最新落盘的配置应用——否则两个 POST 到达顺序无保证，可能按旧规则应用
    await enqueueSave(async () => {})
    const res = await applyPcAutoRule()
    void queryClient.invalidateQueries({ queryKey: pcQueryKeys.priority() })
    return res
  }, [enqueueSave, queryClient])

  return {
    t,
    overview: overview.data,
    unified,
    rows,
    rate,
    window,
    custom,
    scoreDim,
    channels,
    refreshInterval,
    refreshStatus,
    refreshing,
    priority: priority.data,
    priorityPending: priority.isPending,
    priorityEntries,
    effRatio,
    actualOf,
    scoreOf,
    changeWindow,
    changeCustom,
    changeScoreDim,
    changeScoreSource,
    changeChannels,
    changeRefreshInterval,
    triggerRefresh,
    addToPriority,
    removeFromPriority,
    movePriority,
    saveAutoRule,
    applyAutoRule,
  }
}

export type PriceCompareState = ReturnType<typeof usePriceCompare>
