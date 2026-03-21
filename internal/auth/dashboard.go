package auth

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"time"
)

// DashboardGate returns middleware that gates dashboard access based on AuthConfig.
func DashboardGate(cfg AuthConfig, sm *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.DashboardMode == "off" {
				next.ServeHTTP(w, r)
				return
			}

			if sm != nil && sm.ValidSession(r) {
				next.ServeHTTP(w, r)
				return
			}

			switch cfg.DashboardMode {
			case "basic":
				if r.Method == http.MethodPost {
					handleBasicLogin(w, r, cfg, sm)
					return
				}
				serveLoginForm(w, "basic")
			case "key":
				token := extractToken(r)
				if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(cfg.DashboardKey)) == 1 {
					if sm != nil {
						sm.SetSession(w)
					}
					next.ServeHTTP(w, r)
					return
				}
				if r.Method == http.MethodPost {
					handleKeyLogin(w, r, cfg, sm)
					return
				}
				serveLoginForm(w, "key")
			}
		})
	}
}

func handleBasicLogin(w http.ResponseWriter, r *http.Request, cfg AuthConfig, sm *SessionManager) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	user := r.FormValue("username")
	pass := r.FormValue("password")

	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.DashboardUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.DashboardPass)) == 1

	if userOK && passOK {
		if sm != nil {
			sm.SetSession(w)
		}
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
	serveLoginForm(w, "basic")
}

func handleKeyLogin(w http.ResponseWriter, r *http.Request, cfg AuthConfig, sm *SessionManager) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	key := r.FormValue("key")

	if subtle.ConstantTimeCompare([]byte(key), []byte(cfg.DashboardKey)) == 1 {
		if sm != nil {
			sm.SetSession(w)
		}
		http.Redirect(w, r, "/ui/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
	serveLoginForm(w, "key")
}

const loginFormStyle = `body{background:#0a0a0a;color:#ededed;font-family:-apple-system,sans-serif;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0}` +
	`.card{background:#171717;border:1px solid #333;border-radius:8px;padding:32px;width:320px}` +
	`h1{font-size:18px;margin:0 0 24px}` +
	`label{display:block;font-size:13px;color:#888;margin-bottom:4px}` +
	`input{width:100%%;box-sizing:border-box;padding:8px;background:#0a0a0a;border:1px solid #333;border-radius:4px;color:#ededed;margin-bottom:16px;font-size:14px}` +
	`button{width:100%%;padding:10px;background:#ededed;color:#0a0a0a;border:none;border-radius:4px;font-size:14px;cursor:pointer}` +
	`button:hover{background:#fff}`

const basicFormFields = `<label>Username</label><input name="username" required autofocus>` +
	`<label>Password</label><input name="password" type="password" required>`

const keyFormFields = `<label>Dashboard Key</label><input name="key" type="password" required autofocus>`

func serveLoginForm(w http.ResponseWriter, mode string) {
	fields := basicFormFields
	if mode == "key" {
		fields = keyFormFields
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Waylog - Login</title><style>%s</style></head>`+
		`<body><div class="card"><h1>Waylog Dashboard</h1><form method="POST">%s`+
		`<button type="submit">Sign in</button></form></div></body></html>`,
		loginFormStyle, fields)
}

// SessionCheckFunc returns a SessionChecker that validates requests against the session manager.
func SessionCheckFunc(sm *SessionManager) SessionChecker {
	if sm == nil {
		return nil
	}
	return func(r *http.Request) bool {
		return sm.ValidSession(r)
	}
}

// DefaultSessionMaxAge is 24 hours.
const DefaultSessionMaxAge = 24 * time.Hour
