// A stand-in for XMLHttpRequest: uploads resolve on the next tick with the scripted
// reply, so a test can watch files go up one at a time.
export type XhrReply = { status: number; body: unknown } | { error: true }
export type Sent = { url: string; file: File; completedBefore: number }

export function stubXhr(replies: XhrReply[]) {
  const sent: Sent[] = []
  let completed = 0
  class FakeXHR {
    status = 0
    statusText = ''
    responseText = ''
    responseType = ''
    upload = { onprogress: null as null | ((e: ProgressEvent) => void) }
    onload: null | (() => void) = null
    onerror: null | (() => void) = null
    private url = ''
    open(_method: string, url: string) {
      this.url = url
    }
    setRequestHeader() {}
    send(form: FormData) {
      const file = form.get('image') as File
      const n = sent.length
      sent.push({ url: this.url, file, completedBefore: completed })
      const r = replies[n] ?? { status: 500, body: { error: 'internal', message: 'no scripted reply' } }
      setTimeout(() => {
        this.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 2 } as ProgressEvent)
        completed++
        if ('error' in r) {
          this.onerror?.()
          return
        }
        this.status = r.status
        this.responseText = JSON.stringify(r.body)
        this.onload?.()
      }, 0)
    }
  }
  vi.stubGlobal('XMLHttpRequest', FakeXHR)
  return sent
}

export const pngFile = (name: string) => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: 'image/png' })
