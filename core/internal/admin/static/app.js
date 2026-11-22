'use strict'

const token = window.ADMIN_TOKEN
const $ = id => document.getElementById(id)
const esc = value => String(value ?? '').replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]))
let state = null
let view = 'overview'
let logCursor = 0
let keyVisible = false

async function api (path, options = {}) {
  const headers = { 'X-ALLAIIS-Admin': token }
  if (options.body) headers['Content-Type'] = 'application/json'
  const response = await fetch(path, { ...options, headers })
  const body = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(body.error || response.statusText)
  return body
}

function toast (text, bad = false) {
  const node = $('toast')
  node.textContent = text
  node.className = 'toast' + (bad ? ' bad' : '')
  node.hidden = false
  clearTimeout(toast.timer)
  toast.timer = setTimeout(() => { node.hidden = true }, 4200)
}

function show (id, text) {
  const node = $(id)
  node.textContent = text
  node.hidden = false
}

function stamp (millis) {
  return new Date(millis).toLocaleTimeString()
}

function switchView (next) {
  view = next
  for (const button of document.querySelectorAll('.nav')) {
    button.toggleAttribute('aria-current', button.dataset.view === next)
    if (button.dataset.view === next) button.setAttribute('aria-current', 'page')
  }
  for (const section of document.querySelectorAll('.view')) section.hidden = section.id !== 'view-' + next
  $('title').textContent = next[0].toUpperCase() + next.slice(1)
  if (next === 'logs') refreshLogs()
}

function render () {
  const reachable = state.reachable
  $('gateway-pill').textContent = reachable ? 'gateway live' : 'gateway down'
  $('gateway-pill').className = 'pill ' + (reachable ? 'ok' : 'bad')
  $('version').textContent = state.version ? 'v' + state.version : ''
  $('ov-gateway').textContent = reachable ? 'live' : 'down'
  $('ov-listen').textContent = state.listen
  $('ov-models').textContent = state.models.length
  $('ov-requests').textContent = state.usage.total || 0
  $('ov-errors').textContent = (state.usage.errors || 0) + ' errors'
  $('ov-chars').textContent = (state.usage.chars || 0).toLocaleString()
  $('base-url').textContent = 'http://' + state.listen + '/v1'
  $('client-key').textContent = keyVisible ? state.client_key : '•'.repeat(32)
  $('fact-data').textContent = state.data_dir
  $('fact-config').textContent = state.config_path
  $('fact-listen').textContent = state.listen
  if (!reachable) toast('Gateway is not listening yet — aliases still save, requests wait for it.', true)

  const health = $('provider-health')
  health.replaceChildren()
  if (!state.providers.length) health.innerHTML = '<p class="muted">No providers configured.</p>'
  for (const provider of state.providers) {
    const line = document.createElement('div')
    line.className = 'row'
    const bad = provider.cooldown_until && new Date(provider.cooldown_until) > new Date()
    line.innerHTML = '<span class="badge ' + (bad ? 'bad' : 'ok') + '">' + (bad ? 'cooling' : 'ready') + '</span>' +
      '<strong>' + esc(provider.id) + '</strong><span class="muted tiny">' + (provider.web ? 'web transport' : 'API') +
      ' · ' + provider.labels.length + ' credential(s)' + (bad ? ' · until ' + stamp(provider.cooldown_until) : '') + '</span>'
    health.append(line)
  }

  const recent = $('recent').querySelector('tbody')
  recent.replaceChildren()
  for (const event of state.usage.recent || []) {
    const row = recent.insertRow()
    row.innerHTML = '<td>' + stamp(event.time) + '</td><td>' + esc(event.model) + '</td><td>' + esc(event.protocol || 'chat') +
      '</td><td><span class="badge ' + (event.status < 400 ? 'ok' : 'bad') + '">' + event.status + '</span></td><td>' + event.millis + '</td><td>' + event.chars + '</td>'
  }

  const models = $('models').querySelector('tbody')
  models.replaceChildren()
  for (const model of state.models) {
    const row = models.insertRow()
    row.innerHTML = '<td>' + esc(model.id) + '</td><td>' + esc(model.provider) + '</td><td><span class="badge ' + esc(model.protocol) + '">' + (model.protocol || 'chat') +
      '</span></td><td>' + esc(model.privacy) + '</td><td>' + (model.label ? esc(model.label) : '<span class="muted">pool</span>') +
      '</td><td class="wrap muted">' + esc(model.upstream_model) + '</td><td></td>'
    const cell = row.lastElementChild
    const remove = document.createElement('button')
    remove.className = 'danger'
    remove.textContent = 'Delete'
    remove.onclick = async () => {
      if (!confirm('Delete alias ' + model.id + '?')) return
      try { await api('/api/models/delete', { method: 'POST', body: JSON.stringify({ id: model.id }) }); toast('Alias deleted'); refresh() } catch (error) { toast(error.message, true) }
    }
    cell.append(remove)
    if (!model.available) row.firstElementChild.innerHTML = esc(model.id) + ' <span class="badge bad">no key</span>'
  }

  const credentials = $('credential-list')
  credentials.replaceChildren()
  if (!state.providers.some(provider => provider.labels.length)) credentials.innerHTML = '<p class="muted">No credentials stored yet.</p>'
  for (const provider of state.providers) {
    for (const label of provider.labels) {
      const line = document.createElement('div')
      line.className = 'row'
      line.innerHTML = '<span class="badge">' + esc(provider.id) + '</span><strong>' + esc(label) + '</strong>'
      const remove = document.createElement('button')
      remove.className = 'danger'
      remove.textContent = 'Remove'
      remove.onclick = async () => {
        if (!confirm('Remove credential ' + label + ' from ' + provider.id + '?')) return
        try { await api('/api/keys/delete', { method: 'POST', body: JSON.stringify({ provider: provider.id, label }) }); toast('Credential removed'); refresh() } catch (error) { toast(error.message, true) }
      }
      line.append(remove)
      credentials.append(line)
    }
  }

  fillSelect($('model-provider'), state.providers.map(p => p.id), $('model-provider').value)
  fillSelect($('key-provider'), state.providers.map(p => p.id), $('key-provider').value)
  fillSelect($('discover-provider'), state.providers.map(p => p.id), $('discover-provider').value)
  fillSelect($('probe-model'), state.models.filter(model => model.available).map(model => model.id), $('probe-model').value)
  fillSelect($('set-privacy'), state.privacy_levels, state.min_privacy)
  fillLabels()
  fillLabelsFor('discover-provider', 'discover-label')

  const base = 'http://' + state.listen + '/v1'
  $('snip-curl').textContent = 'curl ' + base + '/chat/completions \\\n  -H "Authorization: Bearer ' + state.client_key + '" \\\n  -H "Content-Type: application/json" \\\n  -d \'{"model":"<alias>","messages":[{"role":"user","content":"hi"}]}\''
  $('snip-claude').textContent = 'export ANTHROPIC_BASE_URL=http://' + state.listen + '\nexport ANTHROPIC_API_KEY=' + state.client_key
  $('snip-opencode').textContent = JSON.stringify({ providers: { allaiis: { settings: { baseURL: base, apiKey: '{env:ALLAIIS_API_KEY}' } } } }, null, 2)
}

function fillSelect (select, values, current) {
  const previous = current || select.value
  select.replaceChildren()
  for (const value of values) {
    const option = document.createElement('option')
    option.value = value
    option.textContent = value
    select.append(option)
  }
  if (values.includes(previous)) select.value = previous
}

function fillLabels () {
  const provider = state.providers.find(item => item.id === $('model-provider').value)
  fillSelect($('model-label'), ['', ...(provider ? provider.labels : [])], $('model-label').value)
  $('model-label').firstElementChild.textContent = '(pool, any label)'
}

function fillLabelsFor (providerId, labelId) {
  const provider = state.providers.find(item => item.id === $(providerId).value)
  fillSelect($(labelId), (provider ? provider.labels : []), $(labelId).value)
}

async function refresh () {
  try {
    state = await api('/api/state')
    render()
  } catch (error) {
    $('gateway-pill').textContent = 'panel error'
    $('gateway-pill').className = 'pill bad'
    toast(error.message, true)
  }
}

async function refreshLogs () {
  try {
    const result = await api('/api/logs?since=' + logCursor)
    logCursor = result.last
    const box = $('logs')
    for (const line of result.lines) {
      const node = document.createElement('div')
      node.className = 'log-line ' + line.level
      node.innerHTML = '<span class="t">' + stamp(line.time) + '</span><span class="lvl">' + line.level + '</span><span>' + esc(line.message) + '</span>'
      box.append(node)
    }
    while (box.childElementCount > 800) box.firstElementChild.remove()
    if ($('logs-follow').checked) box.scrollTop = box.scrollHeight
  } catch (error) { /* transient */ }
}

function bind () {
  for (const button of document.querySelectorAll('.nav')) button.onclick = () => switchView(button.dataset.view)
  $('refresh').onclick = refresh
  $('model-provider').onchange = fillLabels
  $('discover-provider').onchange = () => fillLabelsFor('discover-provider', 'discover-label')
  $('key-reveal').onclick = () => { keyVisible = !keyVisible; $('key-reveal').textContent = keyVisible ? 'Hide' : 'Reveal'; render() }
  $('key-copy').onclick = async () => { await navigator.clipboard.writeText(state.client_key); toast('Key copied') }

  $('model-form').onsubmit = async event => {
    event.preventDefault()
    const data = Object.fromEntries(new FormData(event.target))
    try {
      const result = await api('/api/models', { method: 'POST', body: JSON.stringify(data) })
      toast('Alias ' + result.id + (result.added ? ' added' : ' already matched'))
      event.target.reset()
      await refresh()
    } catch (error) { toast(error.message, true) }
  }

  $('key-form').onsubmit = async event => {
    event.preventDefault()
    const data = Object.fromEntries(new FormData(event.target))
    try {
      await api('/api/keys', { method: 'POST', body: JSON.stringify(data) })
      toast('Credential saved')
      event.target.reset()
      await refresh()
    } catch (error) { toast(error.message, true) }
  }

  $('capture-open').onclick = async () => {
    try { show('capture-out', 'Opening the capture browser…'); show('capture-out', (await api('/api/web/chrome', { method: 'POST' })).output || 'Capture browser started') } catch (error) { show('capture-out', error.message) }
  }
  $('capture-run').onclick = async () => {
    const label = $('capture-label').value.trim()
    if (!label) return toast('Enter a label first', true)
    if (!confirm('Sign in inside the capture browser, then continue. Capture now?')) return
    $('capture-run').disabled = true
    show('capture-out', 'Waiting for the signed-in landing page…')
    try {
      const result = await api('/api/web/login', { method: 'POST', body: JSON.stringify({ provider: 'venice-web', label }) })
      show('capture-out', result.output || 'Session captured')
      await refresh()
    } catch (error) { show('capture-out', error.message) } finally { $('capture-run').disabled = false }
  }

  $('rotate').onclick = async () => {
    if (!confirm('Rotate the gateway key? Update every client afterwards.')) return
    try { show('rotate-out', (await api('/api/keys/rotate', { method: 'POST' })).key); keyVisible = true; await refresh() } catch (error) { show('rotate-out', error.message) }
  }

  $('discover-run').onclick = async () => {
    const provider = $('discover-provider').value
    const query = new URLSearchParams({ provider, label: $('discover-label').value, match: $('discover-match').value })
    const list = $('discover-list')
    list.innerHTML = '<p class="muted">Fetching…</p>'
    try {
      const result = await api('/api/discover?' + query)
      list.replaceChildren()
      if (!result.models.length) list.innerHTML = '<p class="muted">No matches.</p>'
      for (const model of result.models) {
        const line = document.createElement('div')
        line.className = 'row'
        line.innerHTML = '<code>' + esc(model.id) + '</code><span class="muted tiny">' + esc(model.name || '') + '</span>'
        const add = document.createElement('button')
        add.className = 'ghost'
        add.textContent = 'Create alias'
        add.onclick = () => {
          $('model-provider').value = provider
          fillLabels()
          $('model-label').value = $('discover-label').value
          $('model-upstream').value = model.id
          $('model-name').value = ''
          switchView('aliases')
        }
        line.append(add)
        list.append(line)
      }
    } catch (error) { list.innerHTML = ''; toast(error.message, true) }
  }

  $('probe-run').onclick = async () => {
    const model = $('probe-model').value
    if (!model) return toast('Add an alias with a key first', true)
    show('probe-out', 'Sending…')
    try {
      const result = await api('/api/probe', { method: 'POST', body: JSON.stringify({ model, prompt: $('probe-prompt').value }) })
      show('probe-out', 'HTTP ' + result.status + ' in ' + result.millis + ' ms' + (result.upstream_model ? ' · upstream ' + result.upstream_model : '') +
        (result.emulated ? ' · emulated: ' + result.emulated : '') + '\n\n' + (result.text || result.error || '(empty)'))
      refresh()
    } catch (error) { show('probe-out', error.message) }
  }

  $('set-privacy-save').onclick = async () => {
    try { await api('/api/settings', { method: 'POST', body: JSON.stringify({ min_privacy: $('set-privacy').value }) }); toast('Privacy floor saved'); refresh() } catch (error) { toast(error.message, true) }
  }

  $('reload').onclick = async () => {
    try { await api('/api/reload', { method: 'POST' }); toast('Config reloaded'); refresh() } catch (error) { show('settings-out', error.message) }
  }
  $('download').onclick = async () => {
    try {
      const result = await api('/api/export')
      const blob = new Blob([JSON.stringify(result, null, 2)], { type: 'application/json' })
      const link = document.createElement('a')
      link.href = URL.createObjectURL(blob)
      link.download = 'allaiis-config.json'
      link.click()
      URL.revokeObjectURL(link.href)
    } catch (error) { show('settings-out', error.message) }
  }
  $('copy-curl').onclick = async () => { await navigator.clipboard.writeText($('snip-curl').textContent); toast('Copied') }
  $('copy-claude').onclick = async () => { await navigator.clipboard.writeText($('snip-claude').textContent); toast('Copied') }
  $('logs-clear').onclick = () => { $('logs').replaceChildren() }
}

bind()
refresh()
setInterval(() => {
  if (view === 'overview' || view === 'aliases' || view === 'keys') refresh()
  if (view === 'logs') refreshLogs()
}, 4000)
