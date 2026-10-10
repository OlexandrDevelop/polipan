// SPDX-License-Identifier: Apache-2.0

// An error from the API. status 0 means the server did not answer.
export class ApiError extends Error {
  status: number
  code: string
  retryAfter?: number
  requestId?: string
  // Extra data of the error, for example details.reason.
  details?: Record<string, unknown>

  constructor(
    status: number,
    code: string,
    message: string,
    retryAfter?: number,
    requestId?: string,
    details?: Record<string, unknown>,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
    this.requestId = requestId
    this.details = details
  }
}

interface Result<T> {
  data?: T
  error?: unknown
  response: Response
}

// Turns an openapi-fetch result into the data, or throws an ApiError.
export async function unwrap<T>(call: Promise<Result<T>>): Promise<T> {
  let result: Result<T>
  try {
    result = await call
  } catch {
    throw new ApiError(0, 'network', 'The server did not answer.')
  }
  if (result.response.ok) return result.data as T
  const body = (result.error ?? {}) as {
    code?: string
    message?: string
    request_id?: string
    details?: Record<string, unknown>
  }
  const wait = Number(result.response.headers.get('Retry-After'))
  throw new ApiError(
    result.response.status,
    body.code ?? 'unknown',
    body.message ?? result.response.statusText,
    wait > 0 ? wait : undefined,
    body.request_id,
    body.details,
  )
}
