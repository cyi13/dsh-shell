package main

import (
	"context"
	"errors"
	"log"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/cyi13/dsh-shell/internal/config"
	"github.com/cyi13/dsh-shell/internal/dshproc"
	"github.com/cyi13/dsh-shell/internal/updater"
)

// restartOverlayJS drops a full-screen "dsh restarting…" overlay into the live
// page. It only acts on an http(s) page (the real dsh UI): the local splash is
// served from wails:// and never loses connectivity, so it needs no overlay.
// Idempotent: re-running while the overlay exists is a no-op.
const restartOverlayJS = `(function(){
  try {
    if (location.protocol !== 'http:' && location.protocol !== 'https:') return;
    if (document.getElementById('dsh-desktop-restarting')) return;
    var st = document.createElement('style');
    st.textContent = '@keyframes dshDesktopSpin{to{transform:rotate(360deg)}}';
    document.head.appendChild(st);
    var d = document.createElement('div');
    d.id = 'dsh-desktop-restarting';
    d.setAttribute('style', [
      'position:fixed','left:0','top:0','right:0','bottom:0',
      'z-index:2147483647','background:#0b0f17',
      'display:flex','flex-direction:column','align-items:center','justify-content:center',
      'color:#e5e7eb','font-family:-apple-system,"PingFang SC","Segoe UI",sans-serif',
      'text-align:center','padding:24px'
    ].join(';'));
    var dot = document.createElement('div');
    dot.setAttribute('style','width:40px;height:40px;border-radius:50%;border:4px solid #26324a;border-top-color:#2f6feb;animation:dshDesktopSpin 1s linear infinite;margin-bottom:20px');
    var t = document.createElement('div');
    t.textContent = 'dsh 正在重启，请稍候…';
    t.setAttribute('style','font-size:16px;letter-spacing:.3px');
    var sub = document.createElement('div');
    sub.textContent = '窗口保持打开，dsh 就绪后自动恢复';
    sub.setAttribute('style','margin-top:10px;font-size:13px;color:#6b7688');
    d.appendChild(dot); d.appendChild(t); d.appendChild(sub);
    document.body.appendChild(d);
  } catch (e) {}
})();`

// navigateJS builds a page script that navigates the window to url while
// keeping the restart experience seamless:
//   - it removes the restarting overlay if one is present;
//   - if the current page is already exactly the target URL (same-port restart
//     with an unchanged token), a location.replace would be a no-op, so it
//     forces a reload to pick up the fresh dsh process;
//   - otherwise it navigates via location.replace (cross-origin safe, unlike
//     v3's runtime SetURL).
func navigateJS(url string) string {
	js := "(function(){try{var el=document.getElementById('dsh-desktop-restarting');if(el)el.remove();}catch(e){}var target=" +
		strconv.Quote(url) +
		";try{if(location.href===target){location.reload();}else{location.replace(target);}}catch(e){location.href=target;}})();"
	return js
}

// navigateFromSplashJS navigates to url ONLY while the window is still on the
// local splash (the wails:// origin). Once the page is a real http(s) dsh page
// it does nothing. This is what a navigation RETRY must use: on a same-process
// restart the dsh SPA may rewrite location while loading, so a "navigate if
// URL differs" retry would wrongly force a second full page load (the
// double-flash after a restart). The retry is only meant as a safety net for
// the very first launch, when the splash has not yet handed over to dsh.
func navigateFromSplashJS(url string) string {
	return "(function(){try{if(location.protocol==='http:'||location.protocol==='https:')return;var target=" +
		strconv.Quote(url) +
		";location.replace(target);}catch(e){}})();"
}

// ShellService is bound to the frontend: it exposes dsh lifecycle, update
// management, and configuration to the settings UI.
type ShellService struct {
	app      *application.App
	cfg      config.Config
	proc     *dshproc.Manager
	updater  *updater.Updater
	window   application.Window // main window: splash -> dsh UI (never leaves dsh once there)
	settings application.Window // settings window: created on demand, destroyed on close (never left hidden)

	// Hooks wired by the tray (not part of the public bind).
	onProcState   func(st dshproc.Status)
	onUpdateState func()

	// closeToTray is the live value of Config.CloseToTray (kept in an atomic so
	// the window-close hook can read it without racing SaveConfig). When true,
	// closing the main window (title-bar close / Cmd+W) hides it to the tray and
	// the app keeps running with dsh serving; when false it really quits.
	closeToTray atomic.Bool

	// autoCheckCancel stops the periodic auto-update-check ticker (restarted by
	// SaveConfig when the interval or the enable flag changes).
	autoCheckCancel context.CancelFunc

	// restartInFlight guards against concurrent restart/update flows (a manual
	// RestartDsh racing an InstallUpdate, or a double click).
	restartInFlight atomic.Bool
}

// beginRestartFlow atomically claims the restart/update flow. Returns false if
// one is already running.
func (s *ShellService) beginRestartFlow() bool {
	return s.restartInFlight.CompareAndSwap(false, true)
}

// endRestartFlow releases the restart/update flow lock.
func (s *ShellService) endRestartFlow() {
	s.restartInFlight.Store(false)
}

// syncRuntimeFlags copies the config fields that drive runtime behaviour into
// their lock-free mirrors. Called at startup and after SaveConfig.
func (s *ShellService) syncRuntimeFlags() {
	s.closeToTray.Store(s.cfg.CloseToTray)
}

// closeToTrayEnabled reports whether closing the main window should hide to
// the tray instead of quitting.
func (s *ShellService) closeToTrayEnabled() bool {
	return s.closeToTray.Load()
}

// mainWindowCloseRequested centralises what a "close the main window" gesture
// (Cmd+W, title-bar close) means: with "close to tray" on it hides the window
// (dsh keeps serving, app stays in the tray); with it off it quits the app.
func (s *ShellService) mainWindowCloseRequested() {
	// If the settings window was open, close it too so we never leave an
	// orphan window floating when the main window goes away.
	s.destroySettings()
	if s.closeToTrayEnabled() {
		log.Printf("[desktop] close main window -> hide to tray (close-to-tray on)")
		if s.window != nil {
			s.window.Hide()
		}
		return
	}
	log.Printf("[desktop] close main window -> quit (close-to-tray off)")
	if s.app != nil {
		go s.app.Quit()
	}
}

// stopAutoUpdateCheck cancels the periodic auto-update-check ticker, if one is
// running.
func (s *ShellService) stopAutoUpdateCheck() {
	if s.autoCheckCancel != nil {
		s.autoCheckCancel()
		s.autoCheckCancel = nil
	}
}

// startAutoUpdateCheck (re)starts the periodic auto-update-check ticker using
// the current config (AutoUpdate flag + UpdateIntervalHours). It always stops
// any previous ticker first, so it is safe to call after SaveConfig too.
func (s *ShellService) startAutoUpdateCheck() {
	s.stopAutoUpdateCheck()
	if !s.cfg.AutoUpdate || s.updater == nil {
		return
	}
	hours := s.cfg.UpdateIntervalHours
	if hours <= 0 {
		hours = 6
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.autoCheckCancel = cancel
	log.Printf("[desktop] auto update check scheduled every %d hour(s)", hours)
	go func() {
		t := time.NewTicker(time.Duration(hours) * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.updater.Check(context.Background()); err != nil {
					log.Printf("[desktop] periodic auto update check: %v", err)
				}
			}
		}
	}()
}

// ServiceStartup initialises the child process and updater when the app boots.
func (s *ShellService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	s.syncRuntimeFlags()

	// Apply "start maximised" once the window is actually on screen. The
	// AppKit zoom does not stick if issued while the window is still being
	// created (that is why we do not use WindowOptions.StartState here).
	if s.cfg.StartMaximized && s.window != nil {
		time.AfterFunc(500*time.Millisecond, func() {
			if s.window.IsMaximised() {
				return
			}
			log.Printf("[desktop] maximising main window at startup")
			s.window.Maximise()
		})
	}

	// Port conflict handling: if the preferred port is taken (by the existing
	// browser GUI, another dsh, etc.) pick a free one so the shell still works.
	port, changed := resolvePort(s.cfg.Port)
	if changed {
		log.Printf("[desktop] port %d in use, using %d instead", s.cfg.Port, port)
	}

	s.proc = dshproc.New(s.cfg.DshBin, s.cfg.HomeDir, port, s.cfg.ExtraArgs,
		func(url string, port int) {
			log.Printf("[desktop] dsh URL ready: %s", url)
			// If enabled, keep the VS Code dsh-sessions side panel in sync with
			// the current URL/token after every (re)start. Guarded: only when
			// the extension is installed and the user already configured the
			// baseUrl key. Runs off the main goroutine; never blocks the UI.
			if s.cfg.SyncVscodeURL {
				go func() {
					home, _ := os.UserHomeDir()
					syncVscodeURL(home, url)
				}()
			}
			// Navigate the whole window to dsh. navigateJS removes any
			// "restarting…" overlay and handles the same-URL reload case so a
			// same-port restart (after an update or a crash) recovers without
			// leaving the window on a stale page. The splash's own inline
			// script also auto-navigates on first launch.
			s.navigateTo(url)
			// Fallback in case the first navigation ran before the WebView was
			// ready. It acts ONLY while the window is still on the local splash
			// (wails://): once it is on a real dsh page it does nothing. Using a
			// "URL differs → navigate" retry here would make a restarted dsh
			// (whose SPA rewrites location during load) load a SECOND time —
			// the double "new session ↔ session" flash.
			time.AfterFunc(1500*time.Millisecond, func() {
				log.Printf("[desktop] retry navigation to %s", url)
				s.navigateFromSplash(url)
			})
		},
		func(st dshproc.Status) {
			if s.onProcState != nil {
				s.onProcState(st)
			}
		},
		func(line string) { log.Printf("%s", line) },
	)
	// When dsh dies on its own (crash/kill), the manager auto-restarts it;
	// drop the restarting overlay first so the live page doesn't show a dead
	// connection during the gap.
	s.proc.OnUnexpectedExit = s.showRestartingOverlay

	// Register the dsh:// deep-link receiver. On macOS, when the shell is
	// already running and the user opens a dsh:// link, LaunchServices delivers
	// a kAEGetURL event to this (first) instance, surfaced here as
	// ApplicationLaunchedWithUrl. (A dsh:// URL that force-launches a second
	// instance instead arrives via the single-instance callback in main.go.)
	if s.app != nil && s.app.Event != nil {
		s.app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(evt *application.ApplicationEvent) {
			if evt == nil || evt.Context() == nil {
				return
			}
			s.handleDeepLink(evt.Context().URL())
		})
	}
	s.updater = updater.New(s.cfg.HomeDir, func(_ updater.State) {
		if s.onUpdateState != nil {
			s.onUpdateState()
		}
	})
	// Apply the configured update channel (alpha by default).
	if s.cfg.Channel == "stable" {
		s.updater.SetChannel("stable")
	}

	if err := s.proc.Start(); err != nil {
		log.Printf("[desktop] start dsh: %v", err)
	}
	// Check once at startup, then keep checking on the configured interval
	// (UpdateIntervalHours, default 6h) while the app runs.
	if s.cfg.AutoUpdate {
		go func() {
			if err := s.updater.Check(ctx); err != nil {
				log.Printf("[desktop] auto update check: %v", err)
			}
		}()
	}
	s.startAutoUpdateCheck()

	// The settings page sends this custom event when the user clicks its
	// close button (X). Because the settings panel is a separate window, the
	// main dsh window underneath is never unloaded — closing is just hiding.
	if s.app != nil && s.app.Event != nil {
		s.app.Event.On("settings:close", func(_ *application.CustomEvent) {
			s.CloseSettings()
		})
	}
	return nil
}

// ShowSettings reveals the dedicated settings window (creating it on first
// use) and focuses it. The main dsh window stays exactly where it is (no
// navigation, no reload).
func (s *ShellService) ShowSettings() {
	s.ensureSettingsWindow()
	if s.settings != nil {
		s.settings.Show()
		s.settings.Focus()
	}
}

// CloseSettings closes (destroys) the settings window and brings the main
// window (which is still showing dsh) back to front.
func (s *ShellService) CloseSettings() {
	s.destroySettings()
	if s.window != nil {
		s.window.Show()
		s.window.Focus()
	}
}

// ServiceShutdown cleans up the dsh child process and background tasks.
func (s *ShellService) ServiceShutdown() error {
	s.stopAutoUpdateCheck()
	s.destroySettings()
	if s.proc != nil {
		s.proc.Cleanup()
	}
	return nil
}

// Status is the aggregate snapshot the UI polls.
type Status struct {
	Dsh     dshproc.Status `json:"dsh"`
	Update  updater.State  `json:"update"`
	Config  config.Config  `json:"config"`
	Version string         `json:"version"`
}

// GetStatus returns the current aggregate status.
func (s *ShellService) GetStatus() Status {
	return Status{
		Dsh:     s.proc.StatusSnapshot(),
		Update:  s.updater.StateSnapshot(),
		Config:  s.cfg,
		Version: "0.1.0",
	}
}

// StartDsh starts the dsh child process.
func (s *ShellService) StartDsh() error {
	return s.proc.Start()
}

// showRestartingOverlay drops the full-screen "restarting…" overlay into the
// window if it is currently showing a live http(s) dsh page (see
// restartOverlayJS). Safe to call from any goroutine.
func (s *ShellService) showRestartingOverlay() {
	if s.window != nil {
		s.window.ExecJS(restartOverlayJS)
	}
}

// navigateTo points the window at url (see navigateJS). It removes the
// restarting overlay and forces a reload when already on the target URL.
func (s *ShellService) navigateTo(url string) {
	if s.window != nil {
		s.window.ExecJS(navigateJS(url))
	}
}

// navigateFromSplash points the window at url only while it is still on the
// local splash page (see navigateFromSplashJS). Used as the navigation retry
// safety net: it must never act once the window is already on a real dsh page,
// or a restarted dsh SPA could be reloaded a second time (double flash).
func (s *ShellService) navigateFromSplash(url string) {
	if s.window != nil {
		s.window.ExecJS(navigateFromSplashJS(url))
	}
}

// navigateIfChanged points the window at url only when it is not already
// there (see navigateChangedJS). Used by ShowDsh and deep links, which must
// navigate across pages but never reload a page that is already showing the
// target URL.
func (s *ShellService) navigateIfChanged(url string) {
	if s.window != nil {
		s.window.ExecJS(navigateChangedJS(url))
	}
}

// restartDshSmooth restarts the dsh child while keeping the window open and
// alive: it drops the "restarting…" overlay first, restarts the child, and the
// onURL handler (ServiceStartup) removes the overlay and re-navigates once dsh
// is ready again. The window never closes and never shows a dead connection.
func (s *ShellService) restartDshSmooth() error {
	s.showRestartingOverlay()
	return s.proc.Restart()
}

// RestartDsh restarts the dsh child process. Because a restart kills the
// running instance and boots it again from the current (possibly just-edited
// or just-updated) profile, it first preflights that the current configuration
// can actually boot. If the preflight fails the user is asked to confirm the
// restart anyway, instead of silently tearing down a working instance.
func (s *ShellService) RestartDsh() error {
	if !s.beginRestartFlow() {
		log.Printf("[desktop] restart already in progress, ignoring")
		return nil
	}
	defer s.endRestartFlow()

	err := s.proc.Preflight(25 * time.Second)
	if err == nil {
		log.Printf("[desktop] restart preflight ok, restarting")
		return s.restartDshSmooth()
	}
	log.Printf("[desktop] restart preflight failed: %v", err)
	msg := "预检发现当前 dsh 配置可能无法启动（插件不兼容或启动报错）。\n\n" +
		"仍要重启当前实例吗？\n\n详情：" + err.Error()
	s.confirmDialog("重启预检未通过", msg, func() {
		if !s.beginRestartFlow() {
			return
		}
		defer s.endRestartFlow()
		if rerr := s.restartDshSmooth(); rerr != nil {
			log.Printf("[desktop] restart dsh (forced): %v", rerr)
		}
	})
	return nil
}

// StopDsh stops the dsh child process.
func (s *ShellService) StopDsh() {
	s.proc.Stop()
}

// navigateChangedJS navigates to url only when the window is not already on
// that exact URL. Used by ShowDsh, which must not reload a page that is
// already the live dsh UI (unlike navigateJS, whose same-URL branch reloads to
// pick up a freshly restarted dsh).
func navigateChangedJS(url string) string {
	return "(function(){try{var el=document.getElementById('dsh-desktop-restarting');if(el)el.remove();}catch(e){}var target=" +
		strconv.Quote(url) +
		";try{if(location.href!==target){location.replace(target);}}catch(e){location.href=target;}})();"
}

// ShowDsh shows the main window and brings it to the front (the dsh UI). It
// also closes the settings window if one was open. No-op navigation-wise: the
// main window never leaves dsh, so there is nothing to reload.
func (s *ShellService) ShowDsh() {
	s.destroySettings()
	if s.window != nil {
		s.window.Show()
		s.window.Focus()
	}
}

// ensureSettingsWindow lazily creates the settings window on first use. It is
// deliberately created on demand and destroyed on close (never kept hidden):
// macOS reopens hidden windows when the Dock icon is clicked while no window
// is visible, so a hidden settings window would pop up next to the main one
// on every reopen. Destroyed, it cannot be reopened by the runtime.
func (s *ShellService) ensureSettingsWindow() {
	if s.settings != nil || s.app == nil {
		return
	}
	win := s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "dsh 设置",
		Width:  660,
		Height: 760,
		URL:    "/?settings=1",
	})
	// Esc and Cmd+W inside the settings window close (destroy) it and return
	// to the main dsh window.
	win.RegisterKeyBinding("escape", func(application.Window) {
		s.CloseSettings()
	})
	win.RegisterKeyBinding("command+w", func(application.Window) {
		s.CloseSettings()
	})
	// Title-bar red close: let it close for real and forget our reference.
	win.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		log.Printf("[desktop] settings window closed")
		s.settings = nil
	})
	s.settings = win
}

// destroySettings closes (destroys) the settings window, if any, and forgets
// it. Safe to call repeatedly.
func (s *ShellService) destroySettings() {
	w := s.settings
	s.settings = nil
	if w != nil {
		w.Close()
	}
}

// handleDeepLink wakes the window and navigates to the dsh web URL that
// corresponds to a dsh:// deep link (see deepLinkTarget). If dsh is not ready
// yet (early launch) the URL is dropped — the caller can retry later.
func (s *ShellService) handleDeepLink(raw string) {
	if raw == "" {
		return
	}
	status := s.proc.StatusSnapshot()
	target, ok := deepLinkTarget(raw, status.URL)
	if !ok {
		log.Printf("[desktop] deep link %q: dsh not ready yet, ignoring", raw)
		return
	}
	s.window.Show()
	s.window.Focus()
	log.Printf("[desktop] deep link %q -> %s", raw, target)
	s.navigateIfChanged(target)
}

// CheckUpdate checks for a dsh update (async).
func (s *ShellService) CheckUpdate() error {
	ctx := context.Background()
	go func() {
		if err := s.updater.Check(ctx); err != nil {
			log.Printf("[desktop] update check: %v", err)
		}
	}()
	return nil
}

// InstallUpdate installs the latest dsh + refreshes profile plugins (async).
//
// Safety: after installing a new kernel it does NOT immediately swap the live
// dsh. dsh versions can be unstable — especially against the current profile
// plugins, or after a plugin was edited into a broken state — so a freshly
// installed kernel may not boot. We first preflight the new build with a
// throwaway instance; only if it boots do we smooth-restart onto it. If the
// preflight fails we KEEP the running instance (no restart) and tell the user,
// so an update can never take down a working shell.
func (s *ShellService) InstallUpdate() error {
	ctx := context.Background()
	go func() {
		if !s.beginRestartFlow() {
			log.Printf("[desktop] restart/update already in progress, ignoring")
			return
		}
		defer s.endRestartFlow()

		if err := s.updater.Install(ctx); err != nil {
			log.Printf("[desktop] update install: %v", err)
			s.notifyDialog("dsh 更新失败", err.Error(), true)
			return
		}

		// The kernel on disk changed (npm -g). Verify the NEW build can boot
		// with the current home/plugins before committing the live process to
		// it. The manager still has the OLD binary path cached until Start
		// re-resolves, so point Preflight at the newly installed binary.
		log.Printf("[desktop] update installed, preflighting new dsh build…")
		if s.proc != nil {
			if err := s.proc.Preflight(60 * time.Second); err != nil {
				log.Printf("[desktop] new dsh build failed preflight: %v", err)
				msg := "新版本 dsh 预检失败，可能是与当前插件不兼容或插件被改坏。\n\n" +
					"已保留当前正在运行的版本，未切换。\n\n详情：" + err.Error()
				s.notifyDialog("dsh 更新未切换", msg, true)
				return
			}
			log.Printf("[desktop] new dsh build preflight ok, restarting…")
		}

		// Kernel updated and verified: restart dsh to pick up the new version.
		// Smooth: the window shows a restarting overlay and re-navigates when
		// dsh is back.
		if err := s.restartDshSmooth(); err != nil {
			log.Printf("[desktop] restart dsh after update: %v", err)
		}
		version := s.updater.StateSnapshot().CurrentVersion
		if version == "" {
			version = "最新版本"
		}
		s.notifyDialog("dsh 更新完成", "已更新到 "+version+"，dsh 已平滑重启（窗口保持打开）。", false)
	}()
	return nil
}

// notifyDialog shows a native message box attached to the main window. Safe to
// call from any goroutine (v3 dialogs dispatch to the main thread internally).
func (s *ShellService) notifyDialog(title, message string, isError bool) {
	if s.app == nil || s.app.Dialog == nil {
		return
	}
	var dlg *application.MessageDialog
	if isError {
		dlg = s.app.Dialog.Error()
	} else {
		dlg = s.app.Dialog.Info()
	}
	dlg.SetTitle(title)
	dlg.SetMessage(message)
	dlg.AttachToWindow(s.window)
	ok := dlg.AddButton("好的")
	dlg.SetDefaultButton(ok)
	dlg.Show()
}

// confirmDialog shows a native question box with "仍执行" and "取消" buttons;
// onConfirm runs when the user chooses to proceed. Safe from any goroutine.
func (s *ShellService) confirmDialog(title, message string, onConfirm func()) {
	if s.app == nil || s.app.Dialog == nil {
		return
	}
	dlg := s.app.Dialog.Question()
	dlg.SetTitle(title)
	dlg.SetMessage(message)
	dlg.AttachToWindow(s.window)
	yes := dlg.AddButton("仍执行")
	dlg.SetDefaultButton(yes)
	yes.OnClick(onConfirm)
	cancel := dlg.AddButton("取消")
	dlg.SetCancelButton(cancel)
	dlg.Show()
}

// GetConfig returns the persisted configuration.
func (s *ShellService) GetConfig() config.Config {
	return s.cfg
}

// SaveConfig persists configuration and applies live changes.
func (s *ShellService) SaveConfig(cfg config.Config) error {
	old := s.cfg
	if err := config.Save(cfg); err != nil {
		return err
	}
	s.cfg = cfg

	// Apply changes that need a dsh restart (smooth — the window stays open
	// and shows a restarting overlay): port, binary, DSH_HOME, extra args.
	needsRestart := false
	if s.proc != nil {
		cur := s.proc.StatusSnapshot()
		if cfg.Port != 0 && cfg.Port != cur.Port {
			log.Printf("[desktop] config port changed to %d", cfg.Port)
			needsRestart = true
		}
		if cfg.DshBin != old.DshBin || cfg.HomeDir != old.HomeDir || !slices.Equal(cfg.ExtraArgs, old.ExtraArgs) {
			log.Printf("[desktop] config dsh bin/home/args changed")
			needsRestart = true
		}
		if needsRestart {
			s.proc.SetConfig(cfg.DshBin, cfg.HomeDir, cfg.Port, cfg.ExtraArgs)
			s.restartDshSmooth()
		}
	}

	// Channel change: re-point the updater and re-check.
	if cfg.Channel != "" && cfg.Channel != old.Channel && s.updater != nil {
		log.Printf("[desktop] update channel set to %q", cfg.Channel)
		if cfg.Channel == "stable" {
			s.updater.SetChannel("stable")
		} else {
			s.updater.SetChannel("alpha")
		}
		go func() {
			if err := s.updater.Check(context.Background()); err != nil {
				log.Printf("[desktop] update check after channel change: %v", err)
			}
		}()
	}

	// Refresh runtime mirrors (close-to-tray etc.) and restart the periodic
	// auto-update-check ticker so a changed interval / enable flag applies.
	s.syncRuntimeFlags()
	s.startAutoUpdateCheck()
	return nil
}

// OpenInBrowser opens the dsh URL in the system browser.
func (s *ShellService) OpenInBrowser() {
	status := s.proc.StatusSnapshot()
	if status.URL == "" {
		return
	}
	if err := s.app.Browser.OpenURL(status.URL); err != nil {
		log.Printf("[desktop] open browser: %v", err)
	}
}

// IsAutoStartEnabled reports whether the shell is registered to launch at
// login (macOS: SMAppService / LaunchAgent).
func (s *ShellService) IsAutoStartEnabled() bool {
	if s.app.Autostart == nil {
		return false
	}
	ok, err := s.app.Autostart.IsEnabled()
	if err != nil {
		log.Printf("[desktop] autostart status: %v", err)
		return false
	}
	return ok
}

// VscodeDshSessionsDetected reports whether the dsh-sessions VS Code extension
// is installed (so the settings panel can show whether URL sync will do
// anything).
func (s *ShellService) VscodeDshSessionsDetected() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return dshSessionsExtensionInstalled(home)
}

// SetAutoStartEnabled registers or unregisters the shell for launch at login.
func (s *ShellService) SetAutoStartEnabled(enabled bool) error {
	if s.app.Autostart == nil {
		return errors.New("autostart not supported on this platform")
	}
	var err error
	if enabled {
		err = s.app.Autostart.Enable()
	} else {
		err = s.app.Autostart.Disable()
	}
	if err != nil {
		return err
	}
	if enabled {
		log.Printf("[desktop] autostart enabled")
	} else {
		log.Printf("[desktop] autostart disabled")
	}
	return nil
}
