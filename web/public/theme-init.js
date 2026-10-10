// SPDX-License-Identifier: Apache-2.0
// Sets data-theme before the first paint (REQ-UI-015). Same-origin file, no inline script.
// Only "system", "light" and "dark" are accepted. Any other stored value means "system".
;(function () {
  var choice = 'system'
  try {
    var stored = localStorage.getItem('polipan.theme')
    if (stored === 'light' || stored === 'dark' || stored === 'system') choice = stored
  } catch {
    // Storage can be blocked. The default is "system".
  }
  var dark =
    choice === 'dark' ||
    (choice === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light')
})()
