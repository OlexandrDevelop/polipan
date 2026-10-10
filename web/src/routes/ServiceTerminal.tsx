// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { useMe } from '@/auth'
import { LargerScreenNotice } from '@/components/LargerScreen'
import { Banner, Button, SelectField, TextField } from '@/components/ui'
import { t } from '@/i18n'
import { accessFor } from '@/permissions'
import type { Service } from '@/scope'

type Status = 'idle' | 'connecting' | 'connected' | 'closed'

// Output is shown as plain text: terminal escape codes are removed.
// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]|\r/g
const MAX_CHARS = 200_000

function execUrl(serviceId: string, command: string, replica: string): string {
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const query = new URLSearchParams({ command })
  if (replica !== '') query.set('replica', replica)
  return `${scheme}://${window.location.host}/api/v1/services/${encodeURIComponent(serviceId)}/exec?${query}`
}

// UI-30: a shell in a running container over the exec WebSocket (execService). It is a simple
// text terminal: the output is a read-only text log and commands are sent line by line.
// Protocol: binary frames carry terminal bytes both ways; a text frame from the server with
// {"type":"exit","code":N} ends the session.
export function ServiceTerminal({
  service,
  environmentProtected,
}: {
  service: Service
  environmentProtected?: boolean
}) {
  const me = useMe()
  const access = accessFor(me.data?.org_role, 'service:exec', environmentProtected)
  const [status, setStatus] = useState<Status>('idle')
  const [output, setOutput] = useState('')
  const [exitCode, setExitCode] = useState<number | null>(null)
  const [command, setCommand] = useState('/bin/sh')
  const [replica, setReplica] = useState('')
  const [line, setLine] = useState('')
  const socket = useRef<WebSocket | null>(null)
  const box = useRef<HTMLPreElement>(null)
  const decoder = useRef(new TextDecoder())

  useEffect(() => () => socket.current?.close(), [])
  useEffect(() => {
    const el = box.current
    if (el) el.scrollTop = el.scrollHeight
  }, [output])

  const append = (text: string) =>
    setOutput((prev) => (prev + text.replace(ANSI, '')).slice(-MAX_CHARS))

  function connect() {
    socket.current?.close()
    setOutput('')
    setExitCode(null)
    setStatus('connecting')
    const ws = new WebSocket(execUrl(service.id ?? '', command.trim() || '/bin/sh', replica))
    ws.binaryType = 'arraybuffer'
    socket.current = ws
    ws.onopen = () => setStatus('connected')
    ws.onmessage = (event: MessageEvent<ArrayBuffer | string>) => {
      if (typeof event.data !== 'string') {
        append(decoder.current.decode(event.data, { stream: true }))
        return
      }
      try {
        const msg = JSON.parse(event.data) as { type?: string; code?: number }
        if (msg.type === 'exit') {
          setExitCode(msg.code ?? null)
          return
        }
      } catch {
        // Not a control message: it is output.
      }
      append(event.data)
    }
    ws.onclose = () => {
      if (socket.current === ws) setStatus('closed')
    }
  }

  function send(text: string) {
    const ws = socket.current
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(text))
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    send(`${line}\n`)
    setLine('')
  }

  if (access === 'hidden') return <Banner>{t('common.no_permission')}</Banner>
  const blocked = access === 'blocked' ? t('common.needs_admin') : undefined
  const live = status === 'connected'
  const replicas = service.placements ?? []

  return (
    <div className="space-y-4">
      <LargerScreenNotice />
      <div className="hidden space-y-4 sm:block">
        <Banner variant="warning">{t('terminal.audited')}</Banner>
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-48 flex-1">
            <TextField
              label={t('terminal.command')}
              autoComplete="off"
              spellCheck={false}
              readOnly={live}
              value={command}
              onChange={(e) => setCommand(e.target.value)}
            />
          </div>
          {replicas.length > 1 && (
            <div className="min-w-40">
              <SelectField
                label={t('terminal.replica')}
                value={replica}
                onChange={(e) => setReplica(e.target.value)}
                disabled={live}
              >
                <option value="">{t('terminal.replica_any')}</option>
                {replicas.map((p) => (
                  <option key={p.id} value={String(p.replica_index ?? 0)}>
                    {t('terminal.replica_n', { n: (p.replica_index ?? 0) + 1 })}
                  </option>
                ))}
              </SelectField>
            </div>
          )}
          {live || status === 'connecting' ? (
            <Button onClick={() => socket.current?.close()}>{t('terminal.disconnect')}</Button>
          ) : (
            <Button variant="primary" reason={blocked} onClick={connect}>
              {status === 'closed' ? t('terminal.reconnect') : t('terminal.connect')}
            </Button>
          )}
        </div>
        <p role="status" className="text-sm">
          {t(`terminal.status.${status}`)}
          {status === 'closed' &&
            exitCode !== null &&
            ` ${t('terminal.exit_code', { code: exitCode })}`}
        </p>
        <pre
          ref={box}
          role="log"
          aria-live="off"
          aria-label={t('terminal.output')}
          tabIndex={0}
          className="h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded-sm border border-border bg-surface p-3 font-mono text-sm"
        >
          {output}
        </pre>
        <form onSubmit={submit} className="flex flex-wrap items-end gap-2">
          <div className="min-w-0 flex-1">
            <TextField
              label={t('terminal.input')}
              hint={t('terminal.input_hint')}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              className="font-mono"
              disabled={!live}
              value={line}
              onChange={(e) => setLine(e.target.value)}
            />
          </div>
          <Button
            type="submit"
            variant="primary"
            reason={live ? undefined : t('terminal.not_connected')}
          >
            {t('terminal.send')}
          </Button>
          <Button
            reason={live ? undefined : t('terminal.not_connected')}
            onClick={() => send('\x03')}
          >
            {t('terminal.ctrl_c')}
          </Button>
          <Button
            reason={live ? undefined : t('terminal.not_connected')}
            onClick={() => send('\t')}
          >
            {t('terminal.tab')}
          </Button>
        </form>
      </div>
    </div>
  )
}
