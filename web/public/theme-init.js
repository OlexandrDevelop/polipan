// SPDX-License-Identifier: Apache-2.0
// Sets data-theme, data-calm and data-fx before the first paint (REQ-UI-015, REQ-UI-039,
// REQ-UI-040). Same-origin file, no inline script. Only known values are accepted: theme is
// "system", "light" or "dark"; calm is "on" or "off"; fx is "off", "scanlines", "glow" or "curve".
// Any other stored value means the default.
;(function () {
  var choice = 'system'
  var calm = 'off'
  var fx = 'off'
  try {
    var stored = localStorage.getItem('polipan.theme')
    if (stored === 'light' || stored === 'dark' || stored === 'system') choice = stored
    if (localStorage.getItem('polipan.calm') === 'on') calm = 'on'
    var storedFx = localStorage.getItem('polipan.fx')
    if (storedFx === 'scanlines' || storedFx === 'glow' || storedFx === 'curve') fx = storedFx
  } catch {
    // Storage can be blocked. The defaults stay.
  }
  var root = document.documentElement
  var dark =
    choice === 'dark' ||
    (choice === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  root.setAttribute('data-theme', dark ? 'dark' : 'light')
  // The OS setting turns Calm mode on, whatever the switch says.
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) calm = 'on'
  root.setAttribute('data-calm', calm)
  root.setAttribute('data-fx', fx)
})()
