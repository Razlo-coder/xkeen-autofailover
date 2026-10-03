import { useEffect, useMemo, useState } from 'react'

function duration(seconds: number) {
    const days = Math.floor(seconds / 86400)
    const hours = Math.floor((seconds % 86400) / 3600)
    const minutes = Math.floor((seconds % 3600) / 60)
    if (days) return `${days} д ${hours} ч ${minutes} мин`
    if (hours) return `${hours} ч ${minutes} мин`
    if (minutes) return `${minutes} мин`
    return `${seconds} с`
}

export function ConnectionUptime({
    since,
    seconds,
    connected,
    restarting,
}: {
    since?: string
    seconds?: number
    connected: boolean
    restarting: boolean
}) {
    const [now, setNow] = useState(Date.now)
    const receivedAt = useMemo(() => Date.now(), [since, seconds])
    useEffect(() => {
        if (!since || !connected || restarting) return
        setNow(Date.now())
        const timer = window.setInterval(() => setNow(Date.now()), 1000)
        return () => window.clearInterval(timer)
    }, [since, connected, restarting])

    if (!since || !connected || restarting) return null
    const started = new Date(since)
    if (Number.isNaN(started.getTime())) return null
    const today = new Date(now)
    const sameDay =
        started.getFullYear() === today.getFullYear() &&
        started.getMonth() === today.getMonth() &&
        started.getDate() === today.getDate()
    const time = started.toLocaleTimeString('ru-RU', {
        hour: '2-digit',
        minute: '2-digit',
    })
    const date = started.toLocaleDateString('ru-RU', {
        day: '2-digit',
        month: '2-digit',
        ...(started.getFullYear() !== today.getFullYear()
            ? { year: 'numeric' }
            : {}),
    })
    // Use the router's duration, so different device clocks do not inflate it.
    const elapsed =
        Math.max(0, seconds ?? 0) +
        Math.floor(Math.max(0, now - receivedAt) / 1000)

    return (
        <span className='flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground tabular-nums'>
            <time
                dateTime={since}
                title={`Подключение подтверждено ${started.toLocaleString('ru-RU')}`}
                className='whitespace-nowrap'
            >
                с {sameDay ? time : `${date}, ${time}`}
            </time>
            <span aria-hidden='true'>·</span>
            <span className='whitespace-nowrap'>
                аптайм {duration(elapsed)}
            </span>
        </span>
    )
}
