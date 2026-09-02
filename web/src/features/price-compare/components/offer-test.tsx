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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Loader2, Zap } from 'lucide-react'

import { api } from '@/lib/api'

export type PcOfferTestResult = {
  ok: boolean
  latency_ms?: number
  served_model?: string
  provider?: string
  error?: string
  key_mask?: string
  prompt_tokens?: number
  completion_tokens?: number
}

// 同一条报价的测试状态键（时段不参与：同渠道同供应商同时段上下档生死一致）
export function pcOfferTestKey(
  channel: string,
  ref: string,
  provider?: string,
  quant?: string
) {
  return `${channel}|${ref}|${provider || ''}|${quant || ''}`
}

// 单条报价的连通性测试按钮：每次点击都发一个 max_tokens=1 的最小请求重新实测
//（refresh=1 绕过服务端 60s 缓存，第二次点击=真重测；成本 ≈ 百万分之一元），
// OpenRouter 报价会钉该行供应商 + 禁 fallback。闪电三态：未测=灰镂空描边、
// 成功=绿实心、失败=红实心；实测延迟由父组件写进表格「延迟」列（onResult 回调）
export function PcOfferTestButton({
  channel,
  ref: modelRef,
  provider,
  quant,
  onResult,
}: {
  channel: string
  ref: string
  provider?: string
  quant?: string
  onResult?: (key: string, result: PcOfferTestResult) => void
}) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<PcOfferTestResult | null>(null)

  const run = async () => {
    setLoading(true)
    try {
      const res = await api.post('/api/price_compare/test_offer?refresh=1', {
        channel,
        ref: modelRef,
        provider: provider || '',
        quant: quant || '',
      })
      const r = res.data?.data ?? { ok: false, error: 'empty response' }
      setResult(r)
      onResult?.(pcOfferTestKey(channel, modelRef, provider, quant), r)
    } catch {
      const r = { ok: false, error: t('Request failed') as string }
      setResult(r)
      onResult?.(pcOfferTestKey(channel, modelRef, provider, quant), r)
    } finally {
      setLoading(false)
    }
  }

  if (loading) {
    return <Loader2 className='text-muted-foreground h-3.5 w-3.5 animate-spin' />
  }

  let title = t(
    'Sends a 1-token request through this exact channel offer (cost ≈ ¥0.000001)'
  ) as string
  if (result) {
    if (result.ok) {
      const lines = [
        `${t('Test connectivity')} ✓`,
        result.served_model ? `${t('Model')}: ${result.served_model}` : null,
        result.provider ? `provider: ${result.provider}` : null,
        result.key_mask ? `key: ${result.key_mask}` : null,
        result.prompt_tokens != null || result.completion_tokens != null
          ? `tokens: ${result.prompt_tokens ?? 0} + ${result.completion_tokens ?? 0}`
          : null,
      ].filter(Boolean)
      title = lines.join('\n')
    } else {
      const keyLine = result.key_mask ? `\nkey: ${result.key_mask}` : ''
      title = `${t('Test connectivity')} ✗\n${result.error || ''}${keyLine}`
    }
  }

  // 闪电三态：未测=灰色镂空描边（不填充）；成功=绿色实心；失败=红色实心
  let zapCls = 'text-muted-foreground hover:text-foreground cursor-pointer'
  let zapFill = ''
  if (result) {
    if (result.ok) {
      zapCls = 'text-emerald-600 dark:text-emerald-400 cursor-pointer'
      zapFill = ' fill-current'
    } else {
      zapCls = 'text-destructive cursor-pointer'
      zapFill = ' fill-current'
    }
  }

  return (
    <button
      type='button'
      tabIndex={-1}
      title={title}
      aria-label={t('Test connectivity') as string}
      className={zapCls}
      onClick={() => void run()}
    >
      <Zap className={`h-3.5 w-3.5${zapFill}`} />
    </button>
  )
}
