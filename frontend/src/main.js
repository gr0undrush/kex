import '@xterm/xterm/css/xterm.css'
import './style.css'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import {
  ListSessions, SaveSession, DeleteSession, GetSettings, SaveSettings,
  PickKeyFile, Connect, Write, Resize, Close, SetClipboard, GetClipboard,
  VaultStatus, Unlock, CreateVault, DeleteLegacyConfig, ChangePassword,
  ChooseVaultFile, ChooseNewVaultPath, ExportVault, Lock,
} from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

const $ = (s) => document.querySelector(s)
let settings = null
let sessions = []
const tabs = new Map()
let active = null

const b64ToBytes = (b64) => Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))

function termOptions() {
  return {
    fontFamily: settings.fontFamily,
    fontSize: settings.fontSize,
    theme: { foreground: settings.foreground, background: settings.background, cursor: settings.foreground },
    cursorBlink: true,
    scrollback: 10000,
  }
}

function applyChrome() {
  document.documentElement.style.setProperty('--bg', settings.background)
}

// ---------- tabs ----------
function activate(id) {
  active = id
  for (const [tid, t] of tabs) {
    const on = tid === id
    t.pane.classList.toggle('active', on)
    t.tabEl.classList.toggle('active', on)
  }
  const t = tabs.get(id)
  if (t) requestAnimationFrame(() => { t.fit.fit(); t.term.focus() })
}

function closeTab(id) {
  const t = tabs.get(id)
  if (!t) return
  t.offs.forEach((off) => off && off())
  Close(id)
  t.term.dispose()
  t.pane.remove()
  t.tabEl.remove()
  tabs.delete(id)
  if (active === id) {
    const last = [...tabs.keys()].pop()
    active = null
    if (last) activate(last)
  }
}

async function openTab(sess, secret = '') {
  const id = crypto.randomUUID()
  const pane = document.createElement('div')
  pane.className = 'pane'
  $('#panes').appendChild(pane)

  const tabEl = document.createElement('div')
  tabEl.className = 'tab'
  tabEl.innerHTML = '<span class="t"></span><button class="x" title="Close">✕</button>'
  tabEl.querySelector('.t').textContent = sess.name
  $('#tabbar').appendChild(tabEl)

  const term = new Terminal(termOptions())
  const fit = new FitAddon()
  term.loadAddon(fit)
  const t = { id, sess, term, fit, pane, tabEl, offs: [], closed: false }
  tabs.set(id, t)

  tabEl.addEventListener('click', () => activate(id))
  tabEl.addEventListener('auxclick', (e) => { if (e.button === 1) closeTab(id) })
  tabEl.querySelector('.x').addEventListener('click', (e) => { e.stopPropagation(); closeTab(id) })

  activate(id)
  term.open(pane)
  fit.fit()

  // Copy on select. Debounced so drag, double-click and triple-click selections all copy.
  let copyTimer = null
  const copySelection = async () => {
    if (!term.hasSelection()) return
    const text = term.getSelection()
    try { await SetClipboard(text) }
    catch (_) { try { await navigator.clipboard.writeText(text) } catch (_) { /* no clipboard */ } }
  }
  term.onSelectionChange(() => {
    clearTimeout(copyTimer)
    copyTimer = setTimeout(copySelection, 120)
  })
  t.offs.push(() => clearTimeout(copyTimer))

  // Paste on right click (capture phase, so the terminal's own handler doesn't interfere).
  const readClipboard = async () => {
    try { return await GetClipboard() }
    catch (_) { try { return await navigator.clipboard.readText() } catch (_) { return '' } }
  }
  pane.addEventListener('contextmenu', async (e) => {
    e.preventDefault()
    e.stopPropagation()
    if (t.closed) return
    const text = await readClipboard()
    if (text) term.paste(text)
  }, true)

  t.offs.push(EventsOn('term:data:' + id, (b64) => term.write(b64ToBytes(b64))))
  t.offs.push(EventsOn('term:closed:' + id, () => {
    t.closed = true
    tabEl.classList.add('dead')
    term.write('\r\n\x1b[33m[connection closed]\x1b[0m\r\n')
  }))
  term.onData((d) => { if (!t.closed) Write(id, d) })
  term.onResize(({ cols, rows }) => { if (!t.closed) Resize(id, cols, rows) })

  term.write(`Connecting to ${sess.username}@${sess.host}:${sess.port}...\r\n`)
  try {
    await Connect(id, sess.id, secret, term.cols, term.rows)
    term.focus()
  } catch (e) {
    const msg = String(e)
    if (msg.includes('PASSPHRASE') || msg.includes('PASSWORD')) {
      closeTab(id)
      const p = await askSecret(msg.includes('PASSWORD') ? 'password' : 'passphrase', msg.includes('WRONG'), sess)
      if (p !== null) openTab(sess, p)
      return
    }
    t.closed = true
    tabEl.classList.add('dead')
    term.write(`\r\n\x1b[31m${msg}\x1b[0m\r\n`)
  }
}

new ResizeObserver(() => {
  const t = tabs.get(active)
  if (t) t.fit.fit()
}).observe($('#panes'))

// ---------- sessions ----------
function renderSessions() {
  const ul = $('#sessions')
  ul.innerHTML = ''
  for (const s of sessions) {
    const li = document.createElement('li')
    li.innerHTML = '<div class="info"><div class="name"></div><div class="sub"></div></div>' +
      '<span class="icons"><button class="edit" title="Edit">✎</button><button class="del" title="Delete">🗑</button></span>'
    li.querySelector('.name').textContent = s.name
    li.querySelector('.sub').textContent = `${s.username}@${s.host}:${s.port}`
    li.addEventListener('click', () => openTab(s))
    li.querySelector('.edit').addEventListener('click', (e) => { e.stopPropagation(); editSession(s) })
    li.querySelector('.del').addEventListener('click', async (e) => {
      e.stopPropagation()
      if (confirm(`Delete session "${s.name}"?`)) {
        sessions = await DeleteSession(s.id)
        renderSessions()
      }
    })
    ul.appendChild(li)
  }
}

let editingId = ''
function editSession(s = { id: '', name: '', host: '', port: 22, username: '', keyPath: '', authMethod: 'key', savePassword: false, legacy: false }) {
  editingId = s.id
  $('#sName').value = s.name
  $('#sHost').value = s.host
  $('#sPort').value = s.port
  $('#sUser').value = s.username
  $('#sKey').value = s.keyPath
  $('#sAuth').value = s.authMethod || 'key'
  $('#sSave').checked = !!s.savePassword
  $('#sLegacy').checked = !!s.legacy
  updateAuthRows()
  $('#sessDlg').showModal()
}

function updateAuthRows() {
  const pw = $('#sAuth').value === 'password'
  $('#sKeyRow').style.display = pw ? 'none' : ''
  $('#sPassRow').style.display = pw ? '' : 'none'
}
$('#sAuth').addEventListener('change', updateAuthRows)

$('#newSession').addEventListener('click', () => editSession())
$('#sCancel').addEventListener('click', () => $('#sessDlg').close())
$('#sBrowse').addEventListener('click', async () => {
  const p = await PickKeyFile()
  if (p) $('#sKey').value = p
})
$('#sessForm').addEventListener('submit', async (e) => {
  e.preventDefault()
  try {
    sessions = await SaveSession({
      id: editingId,
      name: $('#sName').value,
      host: $('#sHost').value,
      port: parseInt($('#sPort').value, 10) || 22,
      username: $('#sUser').value,
      keyPath: $('#sKey').value,
      authMethod: $('#sAuth').value,
      savePassword: $('#sAuth').value === 'password' && $('#sSave').checked,
      legacy: $('#sLegacy').checked,
    })
    renderSessions()
    $('#sessDlg').close()
  } catch (err) {
    alert(String(err))
  }
})

// ---------- settings ----------
$('#settingsBtn').addEventListener('click', () => {
  $('#tFont').value = settings.fontFamily
  $('#tSize').value = settings.fontSize
  $('#tFg').value = settings.foreground
  $('#tBg').value = settings.background
  $('#setDlg').showModal()
})
$('#tCancel').addEventListener('click', () => $('#setDlg').close())
$('#setForm').addEventListener('submit', async (e) => {
  e.preventDefault()
  settings = {
    fontFamily: $('#tFont').value,
    fontSize: parseInt($('#tSize').value, 10) || 14,
    foreground: $('#tFg').value,
    background: $('#tBg').value,
  }
  await SaveSettings(settings)
  settings = await GetSettings()
  applyChrome()
  for (const t of tabs.values()) {
    const o = termOptions()
    t.term.options.fontFamily = o.fontFamily
    t.term.options.fontSize = o.fontSize
    t.term.options.theme = o.theme
    t.fit.fit()
  }
  $('#setDlg').close()
})

// ---------- passphrase prompt ----------
function askSecret(kind, wrong, sess) {
  return new Promise((resolve) => {
    const dlg = $('#passDlg')
    const who = `${sess.username}@${sess.host}`
    $('#passTitle').textContent = wrong ? `Wrong ${kind}, try again (${who})` : (kind === 'password' ? `Password for ${who}` : `Key passphrase for ${who}`)
    $('#pPass').value = ''
    const done = (v) => {
      $('#passForm').onsubmit = null
      $('#pCancel').onclick = null
      dlg.close()
      resolve(v)
    }
    $('#passForm').onsubmit = (e) => { e.preventDefault(); done($('#pPass').value) }
    $('#pCancel').onclick = () => done(null)
    dlg.showModal()
    $('#pPass').focus()
  })
}

// ---------- shortcuts ----------
window.addEventListener('keydown', (e) => {
  if (e.ctrlKey && e.shiftKey && e.key === 'W' && active) { e.preventDefault(); closeTab(active) }
  if (e.ctrlKey && e.key === 'Tab') {
    e.preventDefault()
    const ids = [...tabs.keys()]
    if (ids.length) activate(ids[(ids.indexOf(active) + (e.shiftKey ? -1 : 1) + ids.length) % ids.length])
  }
})

// ---------- vault (master password) ----------
async function loadAll() {
  settings = await GetSettings()
  applyChrome()
  sessions = await ListSessions()
  renderSessions()
}

const cleanErr = (e) => String(e).replace(/^Error:\s*/, '')

async function showVault(errMsg = '') {
  const st = await VaultStatus()
  const dlg = $('#vaultDlg')
  if (st.unlocked) { if (dlg.open) dlg.close(); return }
  const creating = !st.exists
  $('#vTitle').textContent = creating ? 'Create master password' : 'Unlock KEX'
  $('#vPath').textContent = st.path
  $('#vConfirmRow').style.display = creating ? '' : 'none'
  $('#vHint').style.display = creating ? '' : 'none'
  $('#vNew').textContent = creating ? 'Change location…' : 'Create new vault…'
  $('#vSubmit').textContent = creating ? 'Create vault' : 'Unlock'
  $('#vErr').textContent = errMsg
  $('#vPass').value = ''
  $('#vPass2').value = ''
  if (!dlg.open) dlg.showModal()
  $('#vPass').focus()
}

$('#vaultDlg').addEventListener('cancel', (e) => e.preventDefault())

$('#vaultForm').addEventListener('submit', async (e) => {
  e.preventDefault()
  const pw = $('#vPass').value
  const btn = $('#vSubmit')
  btn.disabled = true
  try {
    const st = await VaultStatus()
    let legacy = ''
    if (!st.exists) {
      if (pw !== $('#vPass2').value) throw new Error('Passwords do not match')
      legacy = await CreateVault(pw)
    } else {
      await Unlock(pw)
    }
    await loadAll()
    $('#vaultDlg').close()
    if (legacy && confirm(`Your sessions were imported from the old unencrypted file:\n${legacy}\n\nDelete that file now?`)) {
      try { await DeleteLegacyConfig(legacy) } catch (err) { alert(cleanErr(err)) }
    }
  } catch (err) {
    await showVault(cleanErr(err))
  } finally {
    btn.disabled = false
  }
})

$('#vOpen').addEventListener('click', async () => {
  try { if (await ChooseVaultFile()) await showVault() } catch (err) { await showVault(cleanErr(err)) }
})
// Pick a location for a new vault; if a vault file already exists there, offer to unlock it instead.
async function pickNewVault() {
  try {
    if (await ChooseNewVaultPath()) {
      const st = await VaultStatus()
      await showVault(st.exists ? 'A vault already exists there. Enter its password to open it.' : '')
      return
    }
  } catch (err) {
    await showVault(cleanErr(err))
    return
  }
  await showVault()
}
$('#vNew').addEventListener('click', pickNewVault)

$('#newVaultBtn').addEventListener('click', async () => {
  if (tabs.size && !confirm('Creating a new vault locks the current one and closes all open connections. Continue?')) return
  for (const id of [...tabs.keys()]) closeTab(id)
  await Lock()
  sessions = []
  renderSessions()
  await pickNewVault()
})

$('#lockBtn').addEventListener('click', async () => {
  if (tabs.size && !confirm('Locking closes all open connections. Continue?')) return
  for (const id of [...tabs.keys()]) closeTab(id)
  await Lock()
  sessions = []
  renderSessions()
  await showVault()
})

$('#exportBtn').addEventListener('click', async () => {
  try {
    const p = await ExportVault()
    if (p) alert(`Encrypted copy saved to:\n${p}\n\nOpen it in another KEX instance with "Open other vault…" on the unlock screen, using the same master password.`)
  } catch (err) { alert(cleanErr(err)) }
})

$('#pwBtn').addEventListener('click', () => {
  for (const i of ['#cpOld', '#cpNew', '#cpNew2']) $(i).value = ''
  $('#cpErr').textContent = ''
  $('#pwDlg').showModal()
})
$('#cpCancel').addEventListener('click', () => $('#pwDlg').close())
$('#pwForm').addEventListener('submit', async (e) => {
  e.preventDefault()
  if ($('#cpNew').value !== $('#cpNew2').value) { $('#cpErr').textContent = 'Passwords do not match'; return }
  try {
    await ChangePassword($('#cpOld').value, $('#cpNew').value)
    $('#pwDlg').close()
    alert('Master password changed.')
  } catch (err) { $('#cpErr').textContent = cleanErr(err) }
})

// ---------- init ----------
showVault()
