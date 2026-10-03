import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { Server } from '@/types'

export function ServerGroupCard({
    name,
    members,
    onSelect,
    loading,
}: {
    name: string
    members: Server[]
    onSelect: () => void
    loading: boolean
}) {
    const active = members.find(member => member.active)
    const reachable = members
        .filter(member => member.latency_ms >= 0)
        .sort((a, b) => a.latency_ms - b.latency_ms)
    const best = reachable[0]
    const eligible = members.some(member => member.automatic_eligible !== false)

    return (
        <Card
            size='sm'
            className={cn(
                'transition-colors',
                active &&
                    'border-emerald-500/50 shadow-[0_0_12px_rgba(16,185,129,0.08)]',
            )}
        >
            <CardContent className='flex items-start justify-between gap-2'>
                <div className='min-w-0 flex-1'>
                    <div className='flex items-center gap-1 text-sm font-medium'>
                        <span className='truncate' title={name}>
                            {name}
                        </span>
                        {active && (
                            <span className='shrink-0 size-2 rounded-full bg-emerald-500' />
                        )}
                    </div>
                    <p className='mt-1 text-xs text-muted-foreground'>
                        {members.length} узлов
                        {active && ` · подключён ${active.name}`}
                    </p>
                    <p className='mt-1 text-xs'>
                        {best ? (
                            <span
                                className={cn(
                                    best.latency_ms <= 300
                                        ? 'text-emerald-400'
                                        : best.latency_ms < 500
                                          ? 'text-amber-400'
                                          : 'text-red-400',
                                )}
                            >
                                Лучший пинг {best.latency_ms} мс
                            </span>
                        ) : (
                            <span className='text-muted-foreground'>
                                Пинг —
                            </span>
                        )}
                    </p>
                    {!eligible && (
                        <p className='mt-1 text-xs text-muted-foreground'>
                            Узлы исключены правилами
                        </p>
                    )}
                </div>
                <Button
                    size='sm'
                    variant='outline'
                    onClick={onSelect}
                    disabled={loading || !eligible}
                    title='Проверить узлы группы и подключить самый быстрый доступный'
                >
                    {active ? 'Лучший узел' : 'Выбрать'}
                </Button>
            </CardContent>
        </Card>
    )
}
