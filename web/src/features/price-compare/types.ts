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

export interface PcRatio {
  cache: number
  output: number
  uncached: number
  cache_write: number
  prompt?: number
}

export interface PcExchange {
  rate: number
  live: boolean
  at: number
}

export interface PcOverview {
  exchange: PcExchange
  ratios: Record<string, PcRatio>
  peak_now: boolean
}

export interface PcVendor {
  id: string
  name: string
  icon: string
}

export interface PcLbScores {
  average: number | null
  reasoning: number | null
  coding: number | null
  agentic_coding: number | null
  math: number | null
  data_analysis: number | null
  language: number | null
  instruction_following: number | null
}

export interface PcArenaEntry {
  name: string
  org?: string
  meta?: string
  rank: number | null
  rank_ci?: number[] | null
  net: number | null
  net_ci: number | null
  success?: number | null
  praise?: number | null
  steer?: number | null
  bash_recovery?: number | null
  tool_halluc?: number | null
  sessions?: number | null
  cost_per_task?: number | null
  out_tokens?: string
  price?: string
}

export interface PcArena {
  name: string
  similarity: number
  code: PcArenaEntry | null
  overall: PcArenaEntry | null
}

export interface PcBenchEntry {
  name: string
  sim: number
  scores: Record<string, number>
}

// 渠道全集：官方三渠道 + 四家中转（与 pc-ui 的 PC_CHANNEL_META 键一致）
export type PcChannel =
  | 'openrouter'
  | 'opencode'
  | 'deepseek'
  | 'apib'
  | 'buzzai'
  | 'apikl'
  | 'ikun'

export interface PcUnifiedRow {
  bench?: Record<string, PcBenchEntry>
  channel: PcChannel
  ref: string
  name: string
  full_name: string
  key: string
  vendor: PcVendor
  currency: 'USD' | 'CNY'
  multiplier: number
  in: number
  out: number
  read: number
  write: number
  provider: string
  quant: string
  throughput: number | null
  latency: number | null
  uptime_1d: number | null
  period: '' | 'off' | 'peak'
  split: boolean
  limit?: number
  note?: string
  group_rate?: number
  dead?: boolean
  lb_name: string | null
  lb_sim: number | null
  lb: PcLbScores | null
  arena: PcArena | null
}

export interface PcOrProviderDetail {
  provider: string
  quantization: string
  prompt: number
  completion: number
  cache_read: number
  latency_p50: number | null
  latency_p75: number | null
  throughput_p50: number | null
  throughput_p75: number | null
  uptime_1d: number | null
  implicit_cache: boolean
  max_prompt_tokens: number | null
}

export interface PcOrPriceDetail {
  model: string
  canonical_slug: string
  providers: PcOrProviderDetail[]
  fetched_at: number
}

export interface PcRefreshStatus {
  running: boolean
  done: number
  total: number
  current: string
  finished: boolean
}

export interface PcUnifiedResponse {
  rows: PcUnifiedRow[]
  at: number
  peak_now: boolean
  rate: number
  refresh: PcRefreshStatus
  bench_meta?: Record<string, string>
}

export interface PcPriorityOffer {
  channel: string
  ref: string
  name: string
  period: string
  provider: string
  quant?: string
}

export interface PcPriorityEntry {
  name: string
  offers: PcPriorityOffer[]
}

export type PcRuleTermType = 'pareto' | 'fixed' | 'filter'

// 一条自动更新规则项；所有规则项入选模型取并集
export interface PcRuleTerm {
  type: PcRuleTermType
  dim?: string        // pareto：基准维度
  max_actual?: number // pareto/filter：实际价上限 ¥/M
  min_score?: number  // pareto/filter：得分下限
  top_models?: number // pareto：前沿内按实际价截断
  models?: string[]   // fixed：OpenRouter 正规名
  score_dim?: string  // filter：得分维度
  include_unscored?: boolean // filter：MinScore>0 时无基准得分的模型也入选
  max_models?: number // filter：最多模型数
  top_n?: number      // 本项选中的模型各保留 N 条报价（0=不限；被多项选中取最小正 N）
}

export interface PcAutoRule {
  terms?: PcRuleTerm[]
  // 以下为旧单模式字段（兼容读取，新保存只写 terms）
  mode?: string
  pareto_dim?: string
  fixed_models?: string[]
  max_actual?: number
  min_score?: number
  score_dim?: string
  max_models?: number
  top_n: number
  sync_channels?: boolean
  route_channels?: boolean
  // 勾选的本地 harness（claude/codex/opencode/zcode）：应用时自动写其配置文件
  harnesses?: string[]
  pareto?: PcParetoTerm[]
}

export interface PcParetoTerm {
  dim: string
  max_actual: number
  min_score: number
  top_models: number
}

export interface PcPriorityConfig {
  window: string
  custom: { out_in: number; cache_hit: number; cache_write: number }
  refresh_interval: string
  score_dim: string
  channels: Record<string, boolean>
  auto_rule?: PcAutoRule
  entries: Record<string, PcPriorityEntry>
}

export interface PcMappingRow {
  channel: PcChannel
  ref: string
  name: string
  target: string
  target_key: string
  source: '' | 'or' | 'family' | 'exact' | 'regex' | 'builtin'
  matched: boolean
}

export interface PcBenchMapRow {
  source: string
  bench_name: string
  target: string
  target_key: string
  sim: number
  pinned: boolean
  matched: boolean
}

export interface PcMappingResponse {
  rows: PcMappingRow[]
  total: number
  unmatched: number
  bench?: { rows: PcBenchMapRow[]; total: number; unmatched: number }
}
