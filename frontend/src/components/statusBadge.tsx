import { cn } from '@/lib/utils'

export function StatusBadge({
    connected,
    xrayRunning,
    latency,
    qualityDegraded = false,
}: {
    connected: boolean
    xrayRunning: boolean
    latency: number
    qualityDegraded?: boolean
}) {
    return (
        <div className='flex flex-wrap items-center gap-3'>
            {/* Статус xray-процесса */}
            <div className='flex items-center gap-1.5'>
                <div
                    className={cn(
                        'w-2 h-2 rounded-full',
                        xrayRunning
                            ? 'bg-emerald-500 shadow-[0_0_6px_rgba(16,185,129,0.6)]'
                            : 'bg-destructive shadow-[0_0_6px_rgba(239,68,68,0.5)]',
                    )}
                />
                <span className='text-xs text-muted-foreground'>
                    {xrayRunning ? 'xray' : 'xray off'}
                </span>
            </div>

            {/* Статус соединения */}
            <div className='flex items-center gap-1.5'>
                <div
                    className={cn(
                        'w-2 h-2 rounded-full',
                        connected && qualityDegraded
                            ? 'bg-amber-500'
                            : connected
                              ? 'bg-emerald-500 shadow-[0_0_6px_rgba(16,185,129,0.6)]'
                              : 'bg-zinc-500',
                    )}
                />
                <span
                    className={cn(
                        'text-xs',
                        connected && qualityDegraded
                            ? 'text-amber-400'
                            : 'text-muted-foreground',
                    )}
                >
                    {connected
                        ? qualityDegraded
                            ? 'высокая задержка'
                            : 'online'
                        : 'offline'}
                </span>
                {connected && latency > 0 && (
                    <span
                        className={cn(
                            'text-xs',
                            latency <= 300
                                ? 'text-emerald-400'
                                : latency < 500
                                  ? 'text-amber-400'
                                  : 'text-red-400',
                        )}
                    >
                        {latency} мс
                    </span>
                )}
            </div>
        </div>
    )
}
