package main

import (
	"embed"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/cyi13/dsh-shell/internal/config"
)

//go:embed all:frontend
var frontendAssets embed.FS

//go:embed build/appicon.png
var appIcon []byte

// setupLogging sends the process log to both stderr and a persistent file so
// that when the app is launched via `open` (Finder/LaunchServices) the stdout
// is not lost and we can diagnose startup issues later. The file location is
// platform-specific (defaultLogPath).
func setupLogging() {
	path := defaultLogPath()
	if path == "" {
		log.Printf("[desktop] setupLogging: no default log path")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[desktop] setupLogging: mkdir %s: %v", path, err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[desktop] setupLogging: open %s: %v", path, err)
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.Printf("[desktop] log file: %s", path)
}

// augmentPath prepends platform-specific directories where dsh/npm/pnpm/node
// typically live. When the app is launched via `open` (Finder / LaunchServices)
// on macOS it does NOT inherit the shell's PATH, so exec.LookPath("dsh") would
// otherwise fail. On Windows the inherited PATH already covers the npm global
// bin; extraPathDirs returns the platform list.
func augmentPath() {
	extra := extraPathDirs()
	if len(extra) == 0 {
		log.Printf("[desktop] PATH untouched (dsh=%v npm=%v)",
			execLookPathOK("dsh"), execLookPathOK("npm"))
		return
	}
	current := os.Getenv("PATH")
	seen := map[string]bool{}
	for _, p := range filepath.SplitList(current) {
		seen[p] = true
	}
	var parts []string
	for _, p := range extra {
		if !seen[p] {
			parts = append(parts, p)
			seen[p] = true
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
	log.Printf("[desktop] augmented PATH (dsh=%v npm=%v)",
		execLookPathOK("dsh"), execLookPathOK("npm"))
}

func execLookPathOK(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func main() {
	setupLogging()
	augmentPath()
	cfg, err := config.Load()
	if err != nil {
		log.Printf("[desktop] config load: %v", err)
	}

	// shellSvc is assigned once the window is created below; the
	// single-instance / deep-link callbacks close over it so a second launch
	// can bring the already-running window back.
	var shellSvc *ShellService

	app := application.New(application.Options{
		Name:        "dsh-desktop",
		Description: "DeepSeek Harness desktop shell",
		Icon:        appIcon,
		Mac: application.MacOptions{
			// Never let the app quit just because its windows were closed.
			// Closing the main window hides it to the tray (when "close to
			// tray" is on) or explicitly quits (when off); closing the last
			// window must never silently kill dsh. Real exits go through the
			// tray "退出" item or Cmd+Q.
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(frontendAssets),
		},
		// Single instance: a second launch (double-click, Dock, a dsh:// deep
		// link while already running) is redirected here instead of starting a
		// second shell fighting over the dsh port. The second instance exits
		// itself; we just surface the running window.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.dsh.desktop",
			ExitCode: 0,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				// A second launch (double-click, Dock, or a dsh:// deep link)
				// was redirected here: surface the already-running window. If
				// the second instance was force-launched with a dsh:// URL
				// (`open -n dsh://…`), v3 appends that URL to Args — forward
				// it so the shell jumps to the requested session.
				deep := ""
				for _, a := range data.Args {
					if len(a) >= 5 && strings.EqualFold(a[:5], "dsh:/") {
						deep = a
						break
					}
				}
				// The callback may fire before shellSvc is wired (app is still
				// starting); retry briefly, then act.
				go func() {
					for i := 0; i < 60 && shellSvc == nil; i++ {
						time.Sleep(100 * time.Millisecond)
					}
					if shellSvc == nil {
						return
					}
					if deep != "" {
						shellSvc.handleDeepLink(deep)
					} else {
						shellSvc.ShowDsh()
					}
				}()
			},
		},
	})

	// Build a custom application menu that deliberately OMITS v3's default
	// File > Close item (Cmd+W). That default item calls
	// currentWindow.Close() — an unconditional programmatic close that bypasses
	// our WindowClosing hook — which, closing the last window, was the actual
	// cause of "Cmd+W force-quits the app". We handle Cmd+W ourselves via a
	// window key binding below (hide to tray / quit, per the setting).
	mainMenu := app.Menu.New()
	mainMenu.AddRole(application.AppMenu)    // About / Hide / Quit (Cmd+Q)
	mainMenu.AddRole(application.EditMenu)   // cut / copy / paste inside dsh
	mainMenu.AddRole(application.ViewMenu)   // reload / zoom / fullscreen
	mainMenu.AddRole(application.WindowMenu) // minimise / zoom
	app.Menu.SetApplicationMenu(mainMenu)

	// The window starts on the local splash (served by the embedded asset
	// server). Once the dsh child prints its URL, the splash page's inline
	// script navigates the whole window to dsh via window.location.replace —
	// page-script navigation to an http:// origin is normal browser semantics
	// that WKWebView honours (unlike v3's runtime SetURL, which cannot leave
	// the wails:// origin).
	//
	// Start state is left normal here; "start maximised" is applied after the
	// window is actually on screen (see ServiceStartup), because issuing the
	// AppKit zoom before the window is visible does not stick reliably.
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "dsh desktop",
		Width:  1280,
		Height: 840,
		URL:    "/",
	})

	// Diagnostic: log when the WebView finishes a navigation, so we can tell
	// whether the splash loaded and whether the dsh navigation happened.
	window.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(_ *application.WindowEvent) {
		log.Printf("[desktop] WebView navigation finished")
	})

	// The settings panel lives in its OWN window so the main window never
	// navigates away from dsh: opening settings does not unload the dsh page.
	// It is created lazily on first open (see ShowSettings) and DESTROYED when
	// closed. Keeping it destroyed while not in use matters on macOS: the
	// runtime reopens hidden windows when the Dock icon is clicked with no
	// visible window, so a hidden settings window would pop up alongside the
	// main window on every reopen. With it destroyed there is nothing to pop.
	shellSvc = &ShellService{
		app:    app,
		cfg:    cfg,
		window: window,
	}
	svc := shellSvc

	// System tray / menu-bar icon: status, open, settings, restart, update,
	// quit; closing the window hides to tray so dsh keeps serving.
	setupTray(app, window, svc)

	// Intercept Cmd+W ourselves (the v3 default File > Close menu item that
	// used to own Cmd+W is gone — see the custom application menu above). With
	// "close to tray" on, Cmd+W hides the main window to the tray; with it off
	// it quits the app (matching "close = quit").
	window.RegisterKeyBinding("command+w", func(application.Window) {
		svc.mainWindowCloseRequested()
	})

	// Clicking the Dock icon brings the main window back (useful when the
	// window was hidden to the tray via close-to-tray / Cmd+W).
	if svc != nil {
		app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
			svc.ShowDsh()
		})
	}

	// The ShellService's ServiceStartup/ServiceShutdown own the dsh child
	// process lifecycle and the auto-update check; register it before Run.
	app.RegisterService(application.NewService(svc))

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
