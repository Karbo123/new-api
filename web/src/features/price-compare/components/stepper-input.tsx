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
import { ChevronDown, ChevronUp } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Input } from '@/components/ui/input'

// new-api 风格的数字步进输入：右侧上下调节钮（暗色），输入框仍可直接键入。
// 系统原生 spinner 已隐藏，按钮步进按 step 增减并 clamp 到 [min, max]。
export function StepperInput({
  value,
  onChange,
  min,
  max,
  step = 1,
  className = 'w-24',
}: {
  value: number
  onChange: (v: number) => void
  min?: number
  max?: number
  step?: number
  className?: string
}) {
  const [text, setText] = useState(String(value))
  // 外部值变化（如载入配置）时同步显示；正在键入的中间态不被打断
  useEffect(() => {
    const n = Number(text)
    if (!(text.trim() !== '' && Number.isFinite(n) && n === value)) {
      setText(String(value))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])

  const clamp = (v: number) => {
    let n = Number.isFinite(v) ? v : 0
    if (min != null) n = Math.max(min, n)
    if (max != null) n = Math.min(max, n)
    return n
  }
  const commit = (raw: string) => {
    setText(raw)
    if (raw.trim() === '') return
    const n = Number(raw.trim())
    if (Number.isFinite(n)) onChange(clamp(n))
  }
  const bump = (dir: 1 | -1) => {
    const n = Math.round((clamp(value) + dir * step) * 1e6) / 1e6
    onChange(clamp(n))
  }

  return (
    <div className={`inline-flex items-stretch ${className}`}>
      <Input
        type='text'
        inputMode='decimal'
        value={text}
        onChange={(e) => commit(e.target.value)}
        onBlur={() => setText(String(value))}
        className='rounded-r-none pr-1 [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none'
      />
      <div className='flex flex-col justify-center rounded-r-md border border-l-0 border-input bg-muted/40'>
        <button
          type='button'
          tabIndex={-1}
          aria-label='increase'
          onClick={() => bump(1)}
          className='text-muted-foreground flex flex-1 items-center justify-center px-1.5 hover:text-foreground'
        >
          <ChevronUp className='h-3 w-3' />
        </button>
        <div className='bg-border h-px w-full' />
        <button
          type='button'
          tabIndex={-1}
          aria-label='decrease'
          onClick={() => bump(-1)}
          className='text-muted-foreground flex flex-1 items-center justify-center px-1.5 hover:text-foreground'
        >
          <ChevronDown className='h-3 w-3' />
        </button>
      </div>
    </div>
  )
}
