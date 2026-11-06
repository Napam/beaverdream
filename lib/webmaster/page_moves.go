package webmaster

import "strings"

// pageMoves reports whether the current page moves something with keys: it
// listens for keys and also reads arrows or WASD or runs a frame loop, or it
// locks the pointer. A keydown that only sends the form on Enter does not count.
func pageMoves(html string) bool {
	if strings.Contains(html, "requestPointerLock") {
		return true
	}
	if !strings.Contains(html, "keydown") && !strings.Contains(html, "keyup") {
		return false
	}
	for _, sign := range []string{"Arrow", "KeyW", "KeyA", "KeyD", "requestAnimationFrame"} {
		if strings.Contains(html, sign) {
			return true
		}
	}
	return false
}
