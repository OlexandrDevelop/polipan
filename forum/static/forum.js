// SPDX-License-Identifier: Apache-2.0
// Two small helpers. The page works without them.
;(function () {
  var root = document.documentElement
  var themeButton = document.querySelector('[data-theme-toggle]')
  if (themeButton) {
    var order = ['system', 'light', 'dark']
    var show = function () {
      var choice = root.getAttribute('data-choice') || 'system'
      themeButton.textContent = '[' + themeButton.getAttribute('data-word') + ':' + choice + ']'
    }
    show()
    themeButton.hidden = false
    themeButton.addEventListener('click', function () {
      var current = root.getAttribute('data-choice') || 'system'
      var next = order[(order.indexOf(current) + 1) % order.length]
      try {
        localStorage.setItem('polipan.forum.theme', next)
      } catch {
        // Storage can be blocked.
      }
      var dark = window.matchMedia('(prefers-color-scheme: dark)').matches
      root.setAttribute('data-choice', next)
      root.setAttribute('data-theme', next === 'system' ? (dark ? 'dark' : 'light') : next)
      show()
    })
  }

})()
