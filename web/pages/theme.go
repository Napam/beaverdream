package pages

import "github.com/a-h/templ"

// themeKey is the localStorage key the toggle writes. The admin has its own
// origin, so the page can never read or overwrite it.
const themeKey = "bd:admin-theme"

// themeToggle flips between light and dark. Both icons are always in the
// button and the dark: variant picks one, so a patched header needs no script
// to restore the label.
func themeToggle() templ.Component {
	return templ.Raw(
		`<button type="button" data-theme-toggle aria-label="toggle dark mode" title="toggle dark mode" class="cursor-pointer rounded-sm p-1.5 text-gray-500 hover:bg-gray-200/70 hover:text-gray-900 dark:text-neutral-500 dark:hover:bg-neutral-800/70 dark:hover:text-neutral-200">
<svg class="h-4 w-4 dark:hidden" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
<svg class="hidden h-4 w-4 dark:block" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
</button>`,
	)
}

// themeScript runs in the head, before the body paints, so a dark admin never
// flashes light on load. The stored choice wins; without one the page follows
// the system. The click is delegated on document because navigation replaces
// the header that holds the button.
func themeScript() templ.Component {
	return templ.Raw(`<script>
(function () {
  var key = '` + themeKey + `';
  var root = document.documentElement;
  var stored = null;
  try { stored = localStorage.getItem(key) } catch (e) {}
  var dark = stored ? stored === 'dark'
    : !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
  root.classList.toggle('dark', dark);
  root.style.colorScheme = dark ? 'dark' : 'light';
  document.addEventListener('click', function (e) {
    var button = e.target && e.target.closest ? e.target.closest('[data-theme-toggle]') : null;
    if (!button) return;
    var next = !root.classList.contains('dark');
    root.classList.toggle('dark', next);
    root.style.colorScheme = next ? 'dark' : 'light';
    try { localStorage.setItem(key, next ? 'dark' : 'light') } catch (e) {}
  });
})();
</script>`)
}
