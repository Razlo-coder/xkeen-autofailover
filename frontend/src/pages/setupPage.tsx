import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import {
    Card,
    CardContent,
    CardHeader,
    CardTitle,
    CardDescription,
} from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { setToken, api } from '@/lib/api'
import type { TokenResponse } from '@/types'

export function SetupPage() {
    const [username, setUsername] = useState('')
    const [password, setPassword] = useState('')
    const [confirmPassword, setConfirmPassword] = useState('')
    const [error, setError] = useState('')

    const setupMutation = useMutation({
        mutationFn: () =>
            api.post<TokenResponse>('/api/auth/setup', { username, password }),
        onSuccess: data => {
            setToken(data.token)
            window.location.href = '/'
        },
        onError: (err: Error) => setError(err.message),
    })

    const handleCredentials = (e: React.FormEvent) => {
        e.preventDefault()
        setError('')
        if (password !== confirmPassword) {
            setError('Пароли не совпадают')
            return
        }
        if (password.length < 8) {
            setError('Пароль должен быть не менее 8 символов')
            return
        }
        setupMutation.mutate()
    }

    return (
        <div className='min-h-screen flex items-center justify-center p-4'>
            <div className='w-full max-w-md'>
                <Card>
                    <CardHeader className='text-center'>
                        <CardTitle className='text-2xl'>XKeen Panel</CardTitle>
                        <CardDescription>Первичная настройка</CardDescription>
                    </CardHeader>
                    <CardContent>
                        {error && (
                            <div className='mb-4 p-3 bg-destructive/10 border border-destructive/30 rounded-lg text-sm text-destructive'>
                                {error}
                            </div>
                        )}

                        <form
                            onSubmit={handleCredentials}
                            className='space-y-4'
                            autoComplete='on'
                        >
                            <div className='space-y-2'>
                                <Label htmlFor='setup-username'>Логин</Label>
                                <Input
                                    id='setup-username'
                                    name='username'
                                    autoComplete='username'
                                    value={username}
                                    onChange={e => setUsername(e.target.value)}
                                    required
                                    autoFocus
                                />
                            </div>
                            <div className='space-y-2'>
                                <Label htmlFor='setup-password'>Пароль</Label>
                                <Input
                                    id='setup-password'
                                    name='password'
                                    type='password'
                                    autoComplete='new-password'
                                    value={password}
                                    onChange={e => setPassword(e.target.value)}
                                    required
                                    minLength={8}
                                />
                            </div>
                            <div className='space-y-2'>
                                <Label htmlFor='setup-confirm'>
                                    Подтверждение пароля
                                </Label>
                                <Input
                                    id='setup-confirm'
                                    name='password_confirm'
                                    type='password'
                                    autoComplete='new-password'
                                    value={confirmPassword}
                                    onChange={e =>
                                        setConfirmPassword(e.target.value)
                                    }
                                    required
                                />
                            </div>
                            <Button
                                type='submit'
                                disabled={setupMutation.isPending}
                                className='w-full'
                            >
                                {setupMutation.isPending
                                    ? 'Создание...'
                                    : 'Создать учётную запись'}
                            </Button>
                        </form>
                    </CardContent>
                </Card>
            </div>
        </div>
    )
}
