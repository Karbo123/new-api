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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

import { cn } from '@/lib/utils'

import {
  PC_REFRESH_STAGES,
  type PriceCompareState,
} from '../use-price-compare'
import { RefreshPipelineCard } from './refresh-pipeline'

// 立即刷新弹窗：两态——勾选（默认全选，可取消部分阶段）与进度（左→右流水线）。
// 触发后弹窗切入流水线进度；中途关闭不中断后台刷新，再次点击按钮可回到进度视图；
// 完成后停在结果视图直到用户重新选择或关闭
export function RefreshDialog({
  pc,
  open,
  onOpenChange,
}: {
  pc: PriceCompareState
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<string[]>(
    PC_REFRESH_STAGES.map((s) => s.key)
  )
  const prevRunning = useRef(false)
  const [showResult, setShowResult] = useState(false)

  // 刷新结束瞬间（running→false）切入结果视图，让用户看到各阶段最终状态
  useEffect(() => {
    if (prevRunning.current && !pc.refreshing) setShowResult(true)
    prevRunning.current = pc.refreshing
  }, [pc.refreshing])

  // 每次打开都回到默认全选（结果视图仅在恰有已完成状态时保留）
  useEffect(() => {
    if (open) setSelected(PC_REFRESH_STAGES.map((s) => s.key))
  }, [open])

  const allKeys = PC_REFRESH_STAGES.map((s) => s.key)
  const toggle = (key: string, checked: boolean) =>
    setSelected((prev) =>
      checked ? [...new Set([...prev, key])] : prev.filter((k) => k !== key)
    )
  const start = () => {
    // 按流水线顺序提交勾选（selected 只表达集合，顺序以阶段表为准）
    const ordered = PC_REFRESH_STAGES.filter((s) =>
      selected.includes(s.key)
    ).map((s) => s.key)
    setShowResult(false)
    void pc.triggerRefresh(ordered)
  }

  // 视图优先级：刷新中 > 刚结束的结果 > 勾选
  let view: 'progress' | 'result' | 'select' = 'select'
  if (pc.refreshing) {
    view = 'progress'
  } else if (showResult && pc.refreshStatus?.finished) {
    view = 'result'
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-xl'>
        <DialogHeader>
          <DialogTitle>
            {view === 'select' ? t('Refresh now') : t('Refreshing…')}
          </DialogTitle>
          <DialogDescription>
            {view === 'select'
              ? t(
                  'Choose the steps to refresh now — unchecked steps are skipped'
                )
              : t(
                  "Refresh keeps running in the background — closing this window won't stop it"
                )}
          </DialogDescription>
        </DialogHeader>

        {view === 'select' ? (
          <div className='space-y-1'>
            {PC_REFRESH_STAGES.map((s, i) => {
              const checked = selected.includes(s.key)
              return (
                <label
                  key={s.key}
                  className={cn(
                    'hover:bg-muted/50 flex cursor-pointer items-start gap-3 rounded-md border p-3 transition-colors',
                    checked ? 'border-primary/40' : 'border-transparent'
                  )}
                >
                  <Checkbox
                    checked={checked}
                    onCheckedChange={(v) => toggle(s.key, v === true)}
                    className='mt-0.5'
                  />
                  <span className='min-w-0 flex-1'>
                    <span className='text-foreground flex items-center gap-2 text-sm font-medium'>
                      <span className='bg-muted text-muted-foreground inline-flex h-5 w-5 items-center justify-center rounded-full text-[11px] tabular-nums'>
                        {i + 1}
                      </span>
                      {t(s.label)}
                    </span>
                    <span className='text-muted-foreground mt-0.5 block text-xs'>
                      {t(s.detail)}
                    </span>
                  </span>
                </label>
              )
            })}
          </div>
        ) : (
          pc.refreshStatus && <RefreshPipelineCard status={pc.refreshStatus} />
        )}

        <DialogFooter className='items-center gap-2'>
          {view === 'select' && (
            <>
              <Button
                variant='ghost'
                size='sm'
                className='mr-auto'
                disabled={selected.length === allKeys.length}
                onClick={() => setSelected(allKeys)}
              >
                {t('Select all')}
              </Button>
              <Button variant='outline' onClick={() => onOpenChange(false)}>
                {t('Cancel')}
              </Button>
              <Button onClick={start} disabled={selected.length === 0}>
                {t('Start refresh')} ({selected.length}/{allKeys.length})
              </Button>
            </>
          )}
          {view === 'progress' && (
            <Button
              variant='outline'
              className='ml-auto'
              onClick={() => onOpenChange(false)}
            >
              {t('Hide')}
            </Button>
          )}
          {view === 'result' && (
            <>
              <Button
                variant='outline'
                className='mr-auto'
                onClick={() => setShowResult(false)}
              >
                {t('Start a new refresh')}
              </Button>
              <Button onClick={() => onOpenChange(false)}>{t('Close')}</Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
