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
import { api } from '@/lib/api'

import type {
  PcMappingResponse,
  PcOverview,
  PcPriorityConfig,
  PcRefreshStatus,
  PcUnifiedResponse,
} from './types'

export async function getPcOverview(): Promise<PcOverview> {
  const res = await api.get('/api/price_compare/overview')
  return res.data.data as PcOverview
}

export async function getPcUnified(): Promise<PcUnifiedResponse> {
  const res = await api.get('/api/price_compare/unified')
  return res.data.data as PcUnifiedResponse
}

export async function getPcPriority(): Promise<PcPriorityConfig> {
  const res = await api.get('/api/price_compare/priority')
  return res.data.data as PcPriorityConfig
}

export async function savePcPriority(
  cfg: PcPriorityConfig
): Promise<PcPriorityConfig> {
  const res = await api.post('/api/price_compare/priority', cfg)
  return res.data.data as PcPriorityConfig
}

export async function postPcRefresh(): Promise<PcRefreshStatus> {
  const res = await api.post('/api/price_compare/refresh')
  return res.data.data as PcRefreshStatus
}

export async function getPcRefreshStatus(): Promise<PcRefreshStatus> {
  const res = await api.get('/api/price_compare/refresh/status')
  return res.data.data as PcRefreshStatus
}

export interface PcAutoRuleApplyResult {
  models: number
  offers: number
  missing: string[]
  channels?: number
  routes?: number
  harness?: string[]
  conn_dropped?: number
}

export async function applyPcAutoRule(): Promise<PcAutoRuleApplyResult> {
  const res = await api.post('/api/price_compare/auto_rule/apply')
  return res.data.data as PcAutoRuleApplyResult
}

export async function getPcMapping(): Promise<PcMappingResponse> {
  const res = await api.get('/api/price_compare/mapping')
  return res.data.data as PcMappingResponse
}

export const pcQueryKeys = {
  all: ['price-compare'] as const,
  overview: () => [...pcQueryKeys.all, 'overview'] as const,
  unified: () => [...pcQueryKeys.all, 'unified'] as const,
  priority: () => [...pcQueryKeys.all, 'priority'] as const,
}
