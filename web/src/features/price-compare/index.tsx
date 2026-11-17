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
import { RefreshCw } from 'lucide-react'

import { SectionPageLayout } from '@/components/layout'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { AliasCard } from './components/alias-card'
import { AutoRuleCard } from './components/auto-rule-card'
import { KeysCard } from './components/keys-card'
import { ModelDialog } from './components/model-dialog'
import { ParetoChart } from './components/pareto-chart'
import { PriorityPanel } from './components/priority-panel'
import { RefreshDialog } from './components/refresh-dialog'
import { RefreshPipelineCard } from './components/refresh-pipeline'
import {
  ExchangeCard,
  RatioCard,
} from './components/ratio-card'
import { SearchPanel } from './components/search-panel'
import {
  PC_REFRESH_INTERVALS,
  usePriceCompare,
} from './use-price-compare'

export function PriceCompare() {
  const { t } = useTranslation()
  const pc = usePriceCompare()
  const [dialogKey, setDialogKey] = useState<string | null>(null)
  const [refreshOpen, setRefreshOpen] = useState(false)

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Price Compare')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='text-muted-foreground text-xs'>
            {t('Auto refresh from network')}
          </span>
          <Select
            value={pc.refreshInterval}
            onValueChange={(v) => v && pc.changeRefreshInterval(v)}
          >
            <SelectTrigger className='w-[130px]'>
              <SelectValue>
                {t(
                  PC_REFRESH_INTERVALS.find((o) => o.value === pc.refreshInterval)
                    ?.label ?? ''
                )}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {PC_REFRESH_INTERVALS.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {t(o.label)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {/* 刷新中不再禁用：点击可回到流水线进度视图（后台刷新不受影响） */}
          <Button size='sm' onClick={() => setRefreshOpen(true)}>
            <RefreshCw
              className={`mr-1 h-3.5 w-3.5${pc.refreshing ? ' animate-spin' : ''}`}
            />
            {pc.refreshing ? t('Refreshing…') : t('Refresh now')}
          </Button>
        </div>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          {/* 抓取流水线进度：放按钮下方独立一行，点击可打开进度弹窗查看大图 */}
          {pc.refreshing && pc.refreshStatus && (
            <div
              className='cursor-pointer'
              onClick={() => setRefreshOpen(true)}
              role='button'
              tabIndex={0}
              onKeyDown={(e) => {
                // 仅响应容器自身的按键：阶段节点按钮的 Enter/空格已切换详情，
                // 不再冒泡弹出刷新弹窗
                if ((e.key === 'Enter' || e.key === ' ') && e.target === e.currentTarget) {
                  setRefreshOpen(true)
                }
              }}
            >
              <RefreshPipelineCard status={pc.refreshStatus} />
            </div>
          )}
          <div className='grid grid-cols-1 gap-4 lg:grid-cols-3'>
            <ExchangeCard pc={pc} />
            <div className='lg:col-span-2'>
              <RatioCard pc={pc} />
            </div>
          </div>
          <AutoRuleCard pc={pc} />
          <KeysCard pc={pc} />
          <ParetoChart pc={pc} onOpenModel={setDialogKey} />
          <SearchPanel pc={pc} />
          <PriorityPanel pc={pc} />
          <AliasCard />
          <ModelDialog pc={pc} openKey={dialogKey} onClose={() => setDialogKey(null)} />
          <RefreshDialog pc={pc} open={refreshOpen} onOpenChange={setRefreshOpen} />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
