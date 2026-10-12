// SPDX-License-Identifier: Apache-2.0
// Sets data-theme before the first paint. Same-origin file, no inline script.
;(function () {
  var choice = 'system'
  try {
    var stored = localStorage.getItem('polipan.forum.theme')
    if (stored === 'light' || stored === 'dark') choice = stored
  } catch {
    // Storage can be blocked. The default stays.
  }
  var dark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches
  document.documentElement.setAttribute(
    'data-theme',
    choice === 'system' ? (dark ? 'dark' : 'light') : choice,
  )
  document.documentElement.setAttribute('data-choice', choice)
})()
