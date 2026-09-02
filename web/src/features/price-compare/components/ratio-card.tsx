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
import * as katex from 'katex'
import 'katex/dist/katex.min.css'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { PC_WINDOW_LABELS } from '../use-price-compare'
import { StepperInput } from './stepper-input'
import type { PriceCompareState } from '../use-price-compare'

const WINDOW_OPTIONS = [
  'day',
  'week',
  'month',
  'halfyear',
  'year',
  'all',
  'custom',
] as const

export function ExchangeCard({ pc }: { pc: PriceCompareState }) {
  const { t } = useTranslation()
  const ex = pc.overview?.exchange
  return (
    <Card className='h-full'>
      <CardHeader>
        <CardTitle>{t('Exchange rate')}</CardTitle>
        <CardDescription>
          {t('Live USD→CNY rate from the network')}
        </CardDescription>
      </CardHeader>
      <CardContent className='flex h-full flex-col items-center justify-center gap-1.5 text-center'>
        <span className='text-muted-foreground text-xs font-medium'>
          USD → CNY
        </span>
        <span className='text-3xl font-bold tabular-nums'>
          {(ex?.rate ?? pc.rate).toFixed(4)}
        </span>
        <span className='text-muted-foreground text-xs'>
          {ex?.live
            ? `${t('updated at')} ${new Date(ex.at * 1000).toLocaleTimeString()}`
            : t('Live rate unavailable, using fallback')}
        </span>
      </CardContent>
    </Card>
  )
}

export function RatioCard({ pc }: { pc: PriceCompareState }) {
  const { t } = useTranslation()
  const R = pc.effRatio()
  const isCustom = pc.window === 'custom'

  const stats = {
    out: (R.output * 100).toFixed(2),
    hit: (R.cache * 100).toFixed(1),
    cw: ((R.cache_write || 0) * 100).toFixed(2),
  }
  const texHtml = katex.renderToString(
    // 注意模板串里要写 \\,（运行时为 LaTeX 薄空格 \,），单反斜杠会被 JS 当转义吃掉变成逗号
    `${R.uncached.toFixed(3)}\\,i + ${R.cache.toFixed(3)}\\,cr + ${(R.cache_write || 0).toFixed(4)}\\,cw + ${R.output.toFixed(4)}\\,o`,
    { displayMode: false, output: 'htmlAndMathml', throwOnError: false }
  )

  return (
    <Card className='h-full'>
      <CardHeader>
        <CardTitle>{t('Actual price ratio settings')}</CardTitle>
        <CardDescription>
          {t(
            'Auto estimation window for input/output/cache ratios from new-api database history'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='text-sm font-medium'>{t('Time window')}</span>
          <Select value={pc.window} onValueChange={(v) => v && pc.changeWindow(v)}>
            <SelectTrigger className='w-[170px]'>
              <SelectValue>
                {t(PC_WINDOW_LABELS[pc.window] || pc.window)}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {WINDOW_OPTIONS.map((w) => (
                <SelectItem key={w} value={w}>
                  {t(PC_WINDOW_LABELS[w])}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {isCustom && (
            <span className='flex flex-wrap items-center gap-2 text-sm'>
              <span className='text-muted-foreground'>
                {t('Output/input ratio')}
              </span>
              <StepperInput
                value={pc.custom.out_in}
                onChange={(v) => pc.changeCustom({ out_in: v })}
                min={0}
                step={1}
              />
              %
              <span className='text-muted-foreground'>
                {t('Cache hit ratio')}
              </span>
              <StepperInput
                value={pc.custom.cache_hit}
                onChange={(v) => pc.changeCustom({ cache_hit: v })}
                min={0}
                max={100}
                step={1}
              />
              %
              <span className='text-muted-foreground'>
                {t('Cache write ratio')}
              </span>
              <StepperInput
                value={pc.custom.cache_write}
                onChange={(v) => pc.changeCustom({ cache_write: v })}
                min={0}
                max={100}
                step={0.1}
              />
              %
            </span>
          )}
        </div>
        <div className='bg-muted/50 text-muted-foreground space-y-2.5 rounded-md p-3'>
          <div className='text-foreground flex flex-wrap items-center justify-center gap-2'>
            <span className='text-sm font-medium'>{t('Actual price')}</span>
            <span className='text-muted-foreground'>=</span>
            <span
              className='overflow-x-auto text-base'
              dangerouslySetInnerHTML={{ __html: texHtml }}
            />
          </div>
          <div className='text-center text-[11px]'>
            {t('Actual price per 1M input tokens')} ·{' '}
            {t('i = input · o = output · cr = cache read · cw = cache write')}
          </div>
          <div className='flex flex-wrap items-center justify-center gap-2'>
            <Badge variant='secondary' className='font-mono text-[11px]'>
              {t('Output')} {stats.out}%
            </Badge>
            <Badge variant='secondary' className='font-mono text-[11px]'>
              {t('Cache hit')} {stats.hit}%
            </Badge>
            <Badge variant='secondary' className='font-mono text-[11px]'>
              {t('Cache write')} {stats.cw}%
            </Badge>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
