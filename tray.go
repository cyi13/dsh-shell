package main

import (
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/cyi13/dsh-shell/internal/dshproc"
)

// setupTray creates the menu-bar (macOS) / system-tray icon, wires its menu to
// the shell service, and makes closing the window hide to tray instead of
// quitting (so dsh keeps serving in the background).
func setupTray(app *application.App, window application.Window, svc *ShellService) *application.SystemTray {
	tray := app.SystemTray.New()

	// Use the app icon as the tray image. On macOS a template icon is ideal,
	// but a full-color icon works too; set it as both normal and dark mode.
	tray.SetIcon(appIcon)
	tray.SetTooltip("dsh desktop")

	// Rebuild the menu each time so dsh status/version stay fresh. We rebuild
	// on the click-to-open (menu shows current state) and after state changes.
	// Declared separately so the checkbox/autostart handlers can reference it
	// (Go closures cannot forward-reference a := variable).
	var buildMenu func() *application.Menu
	buildMenu = func() *application.Menu {
		m := app.NewMenu()

		if svc.proc == nil {
			m.Add("初始化中…").SetEnabled(false)
			m.AddSeparator()
			m.Add("退出").OnClick(func(*application.Context) {
				app.Quit()
			})
			return m
		}

		status := svc.proc.StatusSnapshot()
		if status.Running {
			m.Add("dsh 运行中 · PID " + itoa(status.Pid)).SetEnabled(false)
		} else {
			m.Add("dsh 未运行").SetEnabled(false)
		}
		if status.URL != "" {
			m.Add("打开 dsh 界面").OnClick(func(*application.Context) {
				svc.ShowDsh()
			})
		}
		m.Add("设置面板").OnClick(func(*application.Context) {
			// The settings panel is a separate window: show it without touching
			// the main window's dsh page (no navigation, no reload).
			svc.ShowSettings()
		})
		m.AddSeparator()
		m.Add("重启 dsh").OnClick(func(*application.Context) {
			if err := svc.RestartDsh(); err != nil {
				log.Printf("[tray] restart dsh: %v", err)
			}
		})
		// Launch-at-login toggle. Clicking flips the checkbox (v3 does that
		// before invoking the callback) and re-registers with SMAppService;
		// the menu is rebuilt so the shown state stays truthful.
		autostartItem := m.AddCheckbox("开机自启", svc.IsAutoStartEnabled())
		autostartItem.OnClick(func(*application.Context) {
			enabled := autostartItem.Checked()
			go func() {
				if err := svc.SetAutoStartEnabled(enabled); err != nil {
					log.Printf("[tray] autostart set: %v", err)
				}
				application.InvokeSync(func() { tray.SetMenu(buildMenu()) })
			}()
		})
		hasUpdate := false
		if svc.updater != nil {
			upd := svc.updater.StateSnapshot()
			hasUpdate = upd.UpdateAvail
			if hasUpdate {
				m.Add("安装更新 (" + upd.LatestVersion + ")").OnClick(func(*application.Context) {
					if err := svc.InstallUpdate(); err != nil {
						log.Printf("[tray] install update: %v", err)
					}
				})
			}
		}
		if !hasUpdate {
			m.Add("检查更新").OnClick(func(*application.Context) {
				if err := svc.CheckUpdate(); err != nil {
					log.Printf("[tray] check update: %v", err)
				}
			})
		}
		m.AddSeparator()
		m.Add("退出").OnClick(func(*application.Context) {
			app.Quit()
		})
		return m
	}

	// Defer the initial SetMenu until the app event loop is running; a
	// synchronous SetMenu from setupTray (before app.Run) can leave the WebView
	// blank.
	time.AfterFunc(1500*time.Millisecond, func() {
		application.InvokeSync(func() {
			tray.SetMenu(buildMenu())
		})
	})

	// Refresh the menu when dsh lifecycle changes so the tray stays accurate.
	// SetMenu must run on the main thread, but a blocking InvokeSync from the
	// child-process scanner goroutine can deadlock the WebView initialisation,
	// so the refresh is dispatched asynchronously.
	svc.onProcState = func(st dshproc.Status) {
		go func() {
			time.Sleep(300 * time.Millisecond)
			application.InvokeSync(func() {
				tray.SetMenu(buildMenu())
			})
		}()
	}
	svc.onUpdateState = func() {
		go func() {
			time.Sleep(300 * time.Millisecond)
			application.InvokeSync(func() {
				tray.SetMenu(buildMenu())
			})
		}()
	}

	// Clicking the tray icon toggles the window (show when hidden, hide when
	// shown). Re-enabled after the pty launch fix; the earlier blank-WebView
	// issue was the onState InvokeSync deadlock, not this call.
	tray.ToggleWindow()

	// Closing the main window (title-bar red button): with "close to tray" on
	// it hides to the tray so dsh keeps serving; with it off it quits the app.
	// We always cancel the native close and handle it ourselves — the app is
	// configured (ApplicationShouldTerminateAfterLastWindowClosed=false) to
	// never quit merely because a window closed.
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		svc.mainWindowCloseRequested()
		e.Cancel()
	})

	return tray
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
