import { useCallback, useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { getToken, clearToken } from '@/lib/api'
import type { Server } from '@/types'

export function useStreamLatency() {
    const qc = useQueryClient()
    const [checking, setChecking] = useState(false)
    const esRef = useRef<EventSource | null>(null)

    const cancel = useCallback(() => {
        const es = esRef.current
        esRef.current = null
        es?.close()
        setChecking(false)
    }, [])

    useEffect(
        () => () => {
            esRef.current?.close()
            esRef.current = null
        },
        [],
    )

    const check = useCallback(() => {
        if (esRef.current) return

        const token = getToken()
        if (!token) return

        setChecking(true)
        const es = new EventSource(`/api/servers/check?token=${token}`)
        esRef.current = es

        es.addEventListener('latency', e => {
            if (esRef.current !== es) return
            const { id, latency_ms } = JSON.parse(e.data)
            qc.setQueryData<Server[]>(['servers'], old =>
                old?.map(s => (s.id === id ? { ...s, latency_ms } : s)),
            )
        })

        es.addEventListener('done', () => {
            if (esRef.current === es) cancel()
        })

        es.addEventListener('close', () => {
            if (esRef.current === es) cancel()
        })

        es.onerror = () => {
            if (esRef.current !== es) return
            cancel()

            if (!getToken()) {
                clearToken()
                window.location.href = '/login'
            }
        }
    }, [qc, cancel])

    return { check, checking, cancel }
}
